# coding: utf-8

import re
import sys
from enum import Enum
from getopt import getopt, GetoptError
from multiprocessing import Pool
from queue import Queue
from subprocess import Popen, PIPE
from urllib.parse import urlparse

from json_util import read_json_file, output_to_file

"""
har_converter 的作用是把 chrome devtools 导出的 har 格式 json 文件转换为项目所需的数据
文件，供程序回放这些请求以模拟浏览器的请求过程。

包含的内容如下：
- 所有发送的请求 url。数据来自 entry.request.url
- 发送的请求的先后顺序，即 initiator 字段。通过 _initiator.url 的值来判断是由谁发起的
- 服务器产生该请求的所需要的时间，即 timings.wait 的值减去 1 个 rtt 的值
- 请求的 size 数据，即 response._transferSize 的值
"""


class SchedulerType(Enum):
  NAIVE_ROUND_ROBIN_SCHEDULER = 1
  WEIGHTED_ROUND_ROBIN_SCHEDULER = 2
  DYNAMIC_FIRST_COME_FIRST_SERVE_SCHEDULER = 3
  CLASSIFIED_WEIGHTED_ROUND_ROBIN_SCHEDULER = 4


class StreamStatus(Enum):
  """
  stream 状态
  """
  UNAVAILABLE = 1  # 依赖项尚未满足，无法发送
  ENQUEUED = 2  # 依赖项以满足，已被加入发送队列中
  FINISHED = 3  # 已经发送完毕


class PacketNumberGenerator:
  __packet_number = 0

  def get_packet_number(self):
    return self.__packet_number

  def move_to_next_packet_number(self):
    self.__packet_number += 1


# 当前使用的调度器类型
ACTIVE_SCHEDULER = SchedulerType.NAIVE_ROUND_ROBIN_SCHEDULER
# 一个 QUIC 包所能携带的数据字节数
DATA_BLOCK_SIZE = 1.2 * 1e3
# 负责管理全局共享的数据包编号
packet_number_generator = PacketNumberGenerator()


def get_filtered_entries(original_entries, root_request):
  """
  本方法把原始记录的每一项中提取有用数据，并返回只包含筛选后的字段的 entry 列表

  :param original_entries: 原始请求记录日志
  :param root_request: 根请求 url 地址
  :return: 不重复的 URL 地址列表
  """
  # 从原始数据中过滤出来的 http/https 请求列表
  filtered_entries = []
  # 请求 url 到 resource id 的映射
  request_url_to_resource_id_map = {}
  resource_id = 0
  for entry in original_entries:
    # 根据 request url 进行过滤
    request_url = entry['request']['url']
    request_url_to_resource_id_map[request_url] = resource_id
    # 只提取部分字段
    filtered_entry = {
      # 资源 id
      'resource_id': resource_id,
      # 请求地址
      'request_url': request_url,
      # 在网络上实际传输的字节数
      'response_size': entry['response']['_transferSize'],
      # 尚未被发送的字节数
      'remaining_size': entry['response']['_transferSize'],
      # mime 类型，根据不同类型给予不同的调度策略
      'resource_type': entry['_resourceType'],
      # TTFB 字段，减去 rtt 后即为服务区准备此相应所需要的时间
      'ttfb': float(entry['timings']['wait']),
      # 发起此请求需要满足的依赖项
      'dependencies': get_request_dependencies(
        entry['_initiator'], root_request, request_url_to_resource_id_map),
      # 对端主机名
      'hostname': urlparse(request_url)[1],
      # 请求方式
      'method': entry['request']['method'],
      # 入队时间
      'enqueued_at': None,
      # 开始传输时间
      'started_at': None,
      # 完成时间
      'finished_at': None,
      # 是否已经开始传输
      'is_started': False,

      # 以下是需要计算的字段

      # 排队消耗的时间
      'queued_for': None,
      # 传输数据所消耗的时间
      'transmission_time': None
    }
    filtered_entries.append(filtered_entry)
    resource_id += 1
  return filtered_entries


def get_request_mapping(filtered_entries):
  """
  获取不重复的 request 列表，用于映射请求 url 和资源 id

  :param filtered_entries: 包含所有请求的日志记录，其中的 request 可能会有重复
  :return: request 与资源 id 的映射关系
  """
  request_mapping = {}
  resource_id = 0
  for entry in filtered_entries:
    # 提取请求 url
    request_url = entry['request_url']
    if request_url not in request_mapping:
      # 把该请求 url 添加到映射中
      request_mapping[request_url] = resource_id
      resource_id += 1
  return request_mapping


def get_dependencies_from_call_frames(call_frames):
  """
  根据请求调用栈获取依赖项信息

  :param call_frames: 请求调用栈
  :return: 该请求的所有依赖项
  """
  # 依赖项列表
  dependency_urls = []
  for frame in call_frames:
    dependency_urls.append(frame['url'])
  # 去重后输出
  return list(set(dependency_urls))


def get_request_dependencies(raw_initiator, root_request, request_url_to_resource_id_map):
  """
  获取 request 的依赖项。即这些依赖项都被满足之后才能发起本次请求。

  :param raw_initiator: 日志中的 _initiator 对象
  :param root_request: 根请求 url
  :param request_url_to_resource_id_map: 请求 url 到 resource id 之间的映射
  :return: 此 request 的依赖项列表
  """
  if 'url' in raw_initiator:
    return [request_url_to_resource_id_map[raw_initiator['url']]]

  # 请求类型
  request_initiator_type = raw_initiator['type']

  if request_initiator_type == 'other':
    return [request_url_to_resource_id_map[root_request]]

  # 由 js 脚本发起的请求
  dependencies_url = None
  if request_initiator_type == 'script':
    if len(raw_initiator['stack']['callFrames']) > 0:
      dependencies_url = get_dependencies_from_call_frames(raw_initiator['stack']['callFrames'])
    elif len(raw_initiator['stack']['parent']['callFrames']) > 0:
      dependencies_url = get_dependencies_from_call_frames(raw_initiator['stack']['parent']['callFrames'])

  # 把 url 形式的依赖性转换为 resource id 形式的依赖项
  resource_id_dependencies = []
  for url in dependencies_url:
    resource_id_dependencies.append(request_url_to_resource_id_map[url])
  return resource_id_dependencies


def replace_dependency_url_with_resource_id(filtered_entries, request_mapping):
  """
  用 resource id 来替换原有的依赖项 url

  :param filtered_entries:
  :param request_mapping:
  :return:
  """
  for i in range(len(filtered_entries)):
    for j in range(len(filtered_entries[i]['dependencies'])):
      key = filtered_entries[i]['dependencies'][j]
      resource_id = request_mapping[key]
      filtered_entries[i]['dependencies'][j] = resource_id


def extract_har_json_object(json_object):
  """
  从 HAR 文件中提取有用信息

  :param json_object: HAR 文件所包含的 JSON 对象
  :return: 从 HAR 文件中提取的部分信息
  """
  result = {
    # 发出去的第一个请求
    'root_request': json_object['log']['pages'][0]['title']
  }

  # 原始记录
  original_entries = json_object['log']['entries']
  # 过滤后只包含部分字段的记录列表
  filtered_entries = get_filtered_entries(original_entries, result['root_request'])
  result['filtered_entries'] = filtered_entries

  return result


def naive_round_robin_scheduler(active_stream_queue):
  """
  实现最简单的轮询调度器，在各浏览器中作为后备选项。IE/Edge 使用此调度器

  :param active_stream_queue 活跃 stream 队列
  :return: 当前应该发送哪些 stream，以及它们在数据包中的字节数比例
  """
  recipe = []

  # 已经添加的字节数
  bytes_added = 0
  while bytes_added < DATA_BLOCK_SIZE and not active_stream_queue.empty():
    # 可供此 stream 使用的字节数
    max_data_len = DATA_BLOCK_SIZE - bytes_added
    stream = active_stream_queue.get()
    remaining_data_len = stream['remaining_size']
    if remaining_data_len > max_data_len:
      # 尚未完全发送所有数据，放回队尾等待下一次调度
      bytes_added += max_data_len
      recipe.append({
        'resource_id': stream['resource_id'],
        'length': max_data_len,
        'is_finished': False
      })
      stream['remaining_size'] -= max_data_len
      active_stream_queue.put(stream)
    else:
      # 已经发完全部数据，下一轮将添加后续 stream 的数据
      bytes_added += remaining_data_len
      recipe.append({
        'resource_id': stream['resource_id'],
        'length': remaining_data_len,
        'is_finished': True
      })
      stream['remaining_size'] -= remaining_data_len
    # 标注开始时间
    if not stream['is_started']:
      stream['is_started'] = True
      stream['started_at'] = packet_number_generator.get_packet_number()

  return recipe


def weighted_round_robin_scheduler(active_stream_queue):
  """
  实现加权轮询调度调度器，Safari 使用此调度器

  :return:
  """
  recipe = []
  return recipe


def sort_by_start_time(tmp_list):
  return sorted(tmp_list, key=lambda stream: stream['enqueued_at'])


def get_queue_copied_as_list(target_queue):
  result = []
  while not target_queue.empty():
    result.append(target_queue.get())
  return result


def replace_with_dynamic_queue(active_stream_queue):
  """
  构建 Dynamic FCFS 调度算所需要的动态先进先出队列

  :param active_stream_queue 当前活跃 stream 队列
  :return:
  """
  # 只在复制的队列上操作以避免影响原队列
  queue_copy = get_queue_copied_as_list(active_stream_queue)

  # HTML, CSS, fonts
  highest_priority_list = []
  # JS declared prior to first image, XHR
  high_priority_list = []
  # JS declared after first image
  normal_priority_list = []
  # images, async and defer JS
  low_priority_list = []
  # pushed assets (initially)
  lowest_priority_list = []

  # 首张图片的 resource_id
  first_image = -1
  for stream in queue_copy:
    resource_type = stream['resource_type']
    if resource_type == 'document' or resource_type == 'stylesheet' \
      or resource_type == 'font':
      # HTML, CSS, fonts
      highest_priority_list.append(stream)
      continue
    if resource_type == 'script':
      if first_image == -1:
        # 首张图片之前的 JS
        high_priority_list.append(stream)
        first_image = stream['resource_id']
        continue
      else:
        # 首张图片之后的 JS
        normal_priority_list.append(stream)
        continue
    if resource_type == 'xhr':
      high_priority_list.append(stream)
      continue
    if resource_type == 'image':
      low_priority_list.append(stream)
      continue
    else:
      lowest_priority_list.append(stream)

  highest_priority_list = sort_by_start_time(highest_priority_list)
  high_priority_list = sort_by_start_time(high_priority_list)
  normal_priority_list = sort_by_start_time(normal_priority_list)
  low_priority_list = sort_by_start_time(low_priority_list)
  lowest_priority_list = sort_by_start_time(lowest_priority_list)

  highest_priority_list.extend(high_priority_list)
  highest_priority_list.extend(normal_priority_list)
  highest_priority_list.extend(low_priority_list)
  highest_priority_list.extend(lowest_priority_list)

  for stream in highest_priority_list:
    active_stream_queue.put(stream)


def dynamic_first_come_first_serve_scheduler(active_stream_queue):
  """
  实现动态先进先出调度器，Chrome 使用此调度器

  :return:
  """
  replace_with_dynamic_queue(active_stream_queue)

  recipe = []

  # 已经添加的字节数
  bytes_added = 0
  while bytes_added < DATA_BLOCK_SIZE and not active_stream_queue.empty():
    # 可供此 stream 使用的字节数
    max_data_len = DATA_BLOCK_SIZE - bytes_added
    stream = active_stream_queue.get()
    remaining_data_len = stream['remaining_size']
    if remaining_data_len > max_data_len:
      # 尚未完全发送所有数据，放回队尾等待下一次调度
      bytes_added += max_data_len
      recipe.append({
        'resource_id': stream['resource_id'],
        'length': max_data_len,
        'is_finished': False
      })
      stream['remaining_size'] -= max_data_len
      active_stream_queue.put(stream)
    else:
      # 已经发完全部数据，下一轮将添加后续 stream 的数据
      bytes_added += remaining_data_len
      recipe.append({
        'resource_id': stream['resource_id'],
        'length': remaining_data_len,
        'is_finished': True
      })
      stream['remaining_size'] -= remaining_data_len
    # 标注开始时间
    if not stream['is_started']:
      stream['is_started'] = True
      # stream 开始传输的时间点，用于 FCFS 调度和计算传输时间等指标
      stream['started_at'] = packet_number_generator.get_packet_number()

  return recipe


def classified_weighted_round_robin_scheduler(active_stream_queue):
  """
  实现分类加权调度轮询调度器，Firefox 使用此调度器

  :return:
  """
  recipe = []
  return recipe


def get_packet(recipe):
  return recipe


def compose_next_packet(active_stream_queue, request_status):
  """
  组装下一个数据包

  :param active_stream_queue: 活跃 stream 队列
  :param request_status: stream 的状态
  :return: 下一个数据包
  """
  # 根据不同的调度器来决定数据包可以携带哪些 stream 的数据
  if ACTIVE_SCHEDULER == SchedulerType.NAIVE_ROUND_ROBIN_SCHEDULER:
    # IE/Edge
    recipe = naive_round_robin_scheduler(active_stream_queue)
  elif ACTIVE_SCHEDULER == SchedulerType.WEIGHTED_ROUND_ROBIN_SCHEDULER:
    # Safari
    recipe = weighted_round_robin_scheduler(active_stream_queue)
  elif ACTIVE_SCHEDULER == SchedulerType.DYNAMIC_FIRST_COME_FIRST_SERVE_SCHEDULER:
    # Chrome
    recipe = dynamic_first_come_first_serve_scheduler(active_stream_queue)
  else:
    # FireFox
    recipe = classified_weighted_round_robin_scheduler(active_stream_queue)

  # 按照要求组装数据包
  packet = get_packet(recipe)
  return packet


def replay(extracted_har_object):
  # 一些到数据对象的指针
  filtered_entries = extracted_har_object['filtered_entries']

  # 已经完成了的请求列表，初始化为 request_mapping 的长度，初始值为依赖项未满足状态
  request_status = [StreamStatus.UNAVAILABLE] * len(filtered_entries)
  # 所有依赖项就绪，可以发送数据的 stream 队列
  active_stream_queue = Queue()
  # 记录每一个 packet 中携带的请求请求列表
  replay_log = []

  # 把 root_request 放入活跃队列中，以便调度器能够调度此请求
  root_request = get_request_by_url(filtered_entries, extracted_har_object['root_request'])
  # root_request 的初始状态为已满足
  request_status[root_request['resource_id']] = StreamStatus.ENQUEUED
  # active_stream_queue.put(get_active_stream_block(root_request))
  active_stream_queue.put(root_request)
  root_request['enqueued_at'] = packet_number_generator.get_packet_number()

  # 遍历所有 request 列表以寻找出当前可以回放的所有 request
  while not is_replay_finished(request_status):
    # 筛选出能够发起的请求
    for entry in extracted_har_object['filtered_entries']:
      resource_id = entry['resource_id']
      # 只有处于未就绪状态的 stream 才需要检查依赖项状况，如果依赖项全部被满足，则加入到发送队列中
      if request_status[resource_id] == StreamStatus.UNAVAILABLE and \
        is_dependencies_met(entry['dependencies'], request_status):
        # 把可以发送的对象入队
        # active_stream_queue.put(get_active_stream_block(entry))
        active_stream_queue.put(entry)
        # 修改状态位
        request_status[entry['resource_id']] = StreamStatus.ENQUEUED
        # 记录入队时间
        entry['enqueued_at'] = packet_number_generator.get_packet_number()

    # 从活跃 stream 队列中选取合适的 stream 来组装下一个数据包
    next_packet = compose_next_packet(active_stream_queue, request_status)
    # 标注已完成的 stream
    for frame in next_packet:
      if frame['is_finished']:
        request_status[frame['resource_id']] = StreamStatus.FINISHED
        entry = get_request_by_id(frame['resource_id'], filtered_entries)
        # 标注完成时间
        entry['finished_at'] = packet_number_generator.get_packet_number()
    # 记录此数据包
    replay_log.append(next_packet)
    packet_number_generator.move_to_next_packet_number()

  return replay_log


def get_request_by_id(resource_id, filtered_entries):
  for entry in filtered_entries:
    if entry['resource_id'] == resource_id:
      return entry


def is_dependencies_met(dependencies, request_status):
  """
  是否此请求的所有依赖项都已经被满足

  :param dependencies: 此请求的依赖项数组
  :param request_status: 所有请求的状态
  :return: 是否所有依赖项都已经被满足
  """
  for d in dependencies:
    if request_status[d] != StreamStatus.FINISHED:
      return False
  return True


def is_replay_finished(request_status):
  """
  是否已经回放了所有请求

  :param request_status: 请求的状态
  :return: 是否已经回放了所有请求
  """
  # 默认为已完成状态，如果在遍历过程中遇到尚未回放的请求，则会变成未完成状态
  for status in request_status:
    if not status == StreamStatus.FINISHED:
      return False
  return True


def get_request_by_url(filtered_requests, target_request):
  """
  根据 url 获取请求条目

  :param filtered_requests: 筛选过的请求列表
  :param target_request: 目标请求的 url
  :return: 该请求条目
  """
  for entry in filtered_requests:
    if entry['request_url'] == target_request:
      return entry


def ping(hostname, count=5):
  p = Popen(['ping', hostname, '-c %s' % count], stdout=PIPE)
  ping_result = str(p.communicate()[0])
  # 从 ping 结果中提取系统提供的 rtt 统计数据
  rtt_statistics_str = \
    re.compile('[0-9]+.[0-9]+/[0-9]+.[0-9]+/[0-9]+.[0-9]+/[0-9]+.[0-9]+') \
      .findall(str(ping_result))[0]
  avg_rtt = rtt_statistics_str.split('/')[1]
  return float(avg_rtt)


def get_hostname_average_rtt(extracted_har_object):
  """
  获取所有主机的平均 rtt 数据
  :param extracted_har_object: 数据集对象
  """
  ping_hostname_list = []
  for request in extracted_har_object['filtered_entries']:
    ping_hostname_list.append(request['hostname'])
  # 去重后求各域名的平均 rtt
  ping_hostname_list = list(set(ping_hostname_list))
  ping_result = {}
  # 创建一个等长的资源池
  pool = Pool(len(ping_hostname_list))
  ping_result_list = pool.map(ping, ping_hostname_list)
  for i in range(len(ping_hostname_list)):
    ping_result[ping_hostname_list[i]] = ping_result_list[i]
  # 把收集到的 rtt 数据更新到数据集中，并计算 server_delay
  for entry in extracted_har_object['filtered_entries']:
    entry['server_delay'] = entry['ttfb'] - ping_result[entry['hostname']]


def compute_queue_and_transmission_time(filtered_entries):
  """
  为数据集中的每一个条目计算排队时间和传输时间

  :param filtered_entries: 数据集
  """
  for entry in filtered_entries:
    entry['queued_for'] = entry['started_at'] - entry['enqueued_at']
    entry['transmission_time'] = entry['finished_at'] - entry['started_at'] + 1


def add_new_statistic_sample(resource_type, entry, timing_statistics):
  timing_statistics[resource_type]['request_count'] += 1
  timing_statistics[resource_type]['enqueued_at'] += entry['enqueued_at']
  timing_statistics[resource_type]['queued_for'] += entry['queued_for']
  timing_statistics[resource_type]['started_at'] += entry['started_at']
  timing_statistics[resource_type]['finished_at'] += entry['finished_at']
  timing_statistics[resource_type]['transmission_time'] += entry['transmission_time']

  if entry['started_at'] < timing_statistics[resource_type]['min_start_time']:
    timing_statistics[resource_type]['min_start_time'] = entry['started_at']
  if entry['finished_at'] > timing_statistics[resource_type]['max_finish_time']:
    timing_statistics[resource_type]['max_finish_time'] = entry['finished_at']


def compute_classified_timing_statistics(filtered_entries):
  """
  按照不同的资源类型来计算统计数据

  :param filtered_entries: 数据集
  :return: 统计数据集合
  """
  result_template = {
    'request_count': 0,
    'enqueued_at': 0,
    'queued_for': 0,
    'started_at': 0,
    'finished_at': 0,
    'transmission_time': 0,
    # 同类请求的最早开始时间
    'min_start_time': float('inf'),
    # 同类请求的最晚结束时间
    'max_finish_time': 0
  }

  timing_statistics = {
    # document, script, stylesheet
    'critical_path': result_template.copy(),
    'image': result_template.copy(),
    'xhr': result_template.copy(),
    'other': result_template.copy()
  }

  # 收集所有样本
  for entry in filtered_entries:
    resource_type = entry['resource_type']
    if resource_type == 'document' or resource_type == 'stylesheet' or resource_type == 'script':
      add_new_statistic_sample('critical_path', entry, timing_statistics)
    else:
      add_new_statistic_sample(resource_type, entry, timing_statistics)
  # 计算所有样本的平均数据
  for value in timing_statistics.values():
    request_count = value['request_count']
    value['enqueued_at'] /= request_count
    value['queued_for'] /= request_count
    value['started_at'] /= request_count
    value['finished_at'] /= request_count
    value['transmission_time'] /= request_count

  return timing_statistics


def get_scheduler_name_by_enum_value(enum_value):
  name = None
  if enum_value == SchedulerType.NAIVE_ROUND_ROBIN_SCHEDULER:
    name = 'naive_round_robin_scheduler'
  elif enum_value == SchedulerType.DYNAMIC_FIRST_COME_FIRST_SERVE_SCHEDULER:
    name = 'dynamic_first_come_first_serve_scheduler'
  elif enum_value == SchedulerType.CLASSIFIED_WEIGHTED_ROUND_ROBIN_SCHEDULER:
    name = 'classified_weighted_round_robin_scheduler'
  elif enum_value == SchedulerType.WEIGHTED_ROUND_ROBIN_SCHEDULER:
    name = 'weighted_round_robin_scheduler'
  return name


def replay_requests():
  # 读取 JSON 数据
  file_path = 'har-sample.json'
  json_object = read_json_file(file_path)
  print('json file loaded, file path = <%s>' % file_path)

  # 从 HAR 文件中提取所需信息
  extracted_har_object = extract_har_json_object(json_object)
  # 在获取了所有的依赖项之后，就可以按照依赖项顺序回放各请求了

  # 计算服务器生成响应的延迟
  # print('retrieving average rtt for all hostname')
  # get_hostname_average_rtt(extracted_har_object)

  # 重放请求以获取各项指标
  replay_log = replay(extracted_har_object)
  with open('%s.log' % get_scheduler_name_by_enum_value(ACTIVE_SCHEDULER), 'a') as output_file:
    filtered_entries = extracted_har_object['filtered_entries']
    compute_queue_and_transmission_time(filtered_entries)
    timing_statistics = compute_classified_timing_statistics(filtered_entries)

    # 输出分类统计信息
    for key, value in timing_statistics.items():
      print('timing statistics for resource type=<%s>' % key, file=output_file)
      print('  request count=<%3d>' % value['request_count'], file=output_file)
      print('  enqueued at=<%3.2f>' % value['enqueued_at'], file=output_file)
      print('  queued_for=<%3.2f>' % value['queued_for'], file=output_file)
      print('  started_at=<%3.2f>' % value['started_at'], file=output_file)
      print('  finished_at=<%3.2f>' % value['finished_at'], file=output_file)
      print('  transmission_time=<%3.2f>' % value['transmission_time'], file=output_file)
      print('  min_start_time=<%3.2f>' % value['min_start_time'], file=output_file)
      print('  max_finish_time=<%3.2f>' % value['max_finish_time'], file=output_file)

    print('', file=output_file)

    for i in range(len(filtered_entries)):
      entry = filtered_entries[i]
      print('request <%3d>:<%3.2f>KB, type=<%s>, url = <%s>' % (
        i, entry['response_size'] / 1e3, entry['resource_type'], entry['request_url']), file=output_file)
      print('  enqueued_at=<%4d>, queued_for=<%2d>, started_at=<%4d>, finished_at=<%4d>, transmission_time=<%4d>' %
            (entry['enqueued_at'], entry['queued_for'], entry['started_at'], entry['finished_at'],
             entry['transmission_time']), file=output_file)

    print('', file=output_file)

    for r in replay_log:
      print(r, file=output_file)


def usage():
  print('har-converter can convert har object provided in json file, and output'
        'replay sequence, server delay, and their server dealy in json config'
        'file for qperf.')
  print()
  print('usage: python har_converter.py -i bilibili-config.json -o bilibili-config.json')


def parse_command_line_arguments(argv):
  opts = None

  try:
    opts, _ = getopt(argv, 'hi:o:', ['input=', 'output='])
  except GetoptError:
    usage()

  args = {}

  for opt, arg in opts:
    if opt == '-h':
      usage()
      sys.exit()
    elif opt in ('-i', '--input'):
      args['har_file_path'] = arg
    elif opt in ('-o', '--output'):
      args['output_file_path'] = arg

  return args


def main(argv):
  # 解析命令行参数
  args = parse_command_line_arguments(argv)
  har_file_path = args['har_file_path']
  output_file_path = args['output_file_path']

  print('har_file_path: <%s>' % har_file_path)
  print('output_file_path: <%s>' % output_file_path)

  json_object = read_json_file(har_file_path)
  print('json file loaded, file path = <%s>' % har_file_path)

  # 从 HAR 文件中提取所需信息
  extracted_har_object = extract_har_json_object(json_object)
  # 在获取了所有的依赖项之后，就可以按照依赖项顺序回放各请求了

  # 计算服务器生成响应的延迟
  print('retrieving average rtt for all hostname')
  get_hostname_average_rtt(extracted_har_object)

  # 保存数据到输出文件中
  print('dumping result to config file: <%s>' % output_file_path)
  output_to_file(output_file_path, extracted_har_object)


if __name__ == '__main__':
  main(sys.argv[1:])
