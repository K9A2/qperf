# coding: utf-8

import re
from multiprocessing import Pool
from subprocess import Popen, PIPE
from urllib.parse import urlparse

from python.json_util import read_json_file

"""
har_converter 的作用是把 chrome devtools 导出的 har 格式 json 文件转换为项目所需的数据
文件，供程序回放这些请求以模拟浏览器的请求过程。

包含的内容如下：
- 所有发送的请求 url。数据来自 entry.request.url
- 发送的请求的先后顺序，即 initiator 字段。通过 _initiator.url 的值来判断是由谁发起的
- 服务器产生该请求的所需要的时间，即 timings.wait 的值减去 1 个 rtt 的值
- 请求的 size 数据，即 response._transferSize 的值
"""


def get_filtered_entries(original_entries, root_request):
  """
  本方法把原始记录的每一项中提取有用数据，并返回只包含筛选后的字段的 entry 列表

  :param original_entries: 原始请求记录日志
  :param root_request: 根请求 url 地址
  :return: 不重复的 URL 地址列表
  """
  # 从原始数据中过滤出来的 http/https 请求列表
  filtered_entries = []
  for entry in original_entries:
    # 根据 request url 进行过滤
    request_url = entry['request']['url']
    # 只提取部分字段
    filtered_entry = {
      # 请求地址
      'request_url': request_url,
      # 在网络上实际传输的字节数
      'response_transfer_size': entry['response']['_transferSize'],
      # mime 类型，根据不同类型给予不同的调度策略
      # todo: 是否需要改成 entry._resourceType
      'resource_type': entry['_resourceType'],
      # TTFB 字段，减去 rtt 后即为服务区准备此相应所需要的时间
      'ttfb': float(entry['timings']['wait']),
      # 发起此请求需要满足的依赖项
      'dependencies': get_request_dependencies(entry['_initiator'], root_request),
      # 对端主机名
      'hostname': urlparse(request_url)[1],
      # 请求方式
      'method': entry['request']['method']
    }
    # 只记录 http/https 请求
    filtered_entries.append(filtered_entry)
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


def get_request_dependencies(raw_initiator, root_request):
  """
  获取 request 的依赖项。即这些依赖项都被满足之后才能发起本次请求。

  :param raw_initiator: 日志中的 _initiator 对象
  :param root_request: 根请求 url
  :return: 此 request 的依赖项列表
  """
  if 'url' in raw_initiator:
    return [raw_initiator['url']]

  # 请求类型
  request_initiator_type = raw_initiator['type']

  if request_initiator_type == 'other':
    return [root_request]

  # 由 js 脚本发起的请求
  if request_initiator_type == 'script':
    if len(raw_initiator['stack']['callFrames']) > 0:
      return get_dependencies_from_call_frames(raw_initiator['stack']['callFrames'])
    elif len(raw_initiator['stack']['parent']['callFrames']) > 0:
      return get_dependencies_from_call_frames(
        raw_initiator['stack']['parent']['callFrames'])


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

  # 请求 url 与资源 id 之间的映射字典
  result['request_mapping'] = get_request_mapping(filtered_entries)

  # 用 resource id 来替换依赖项 url
  replace_dependency_url_with_resource_id(filtered_entries, result['request_mapping'])

  return result


def replay(extracted_har_object):
  # 请求 url 到 resource id 的映射
  request_mapping = extracted_har_object['request_mapping']
  # 已经完成了的请求列表，初始化为 request_mapping 的长度，值为 false
  request_status = [False] * len(request_mapping)
  # 每一个 time_slot 回放的请求列表
  replay_log = []
  # root_request 的初始状态为已满足
  request_status[0] = True
  replay_log.append([extracted_har_object['root_request']])

  # 每个 time slot 发送一个数据包
  time_slot = 1

  # 遍历所有 request 以寻找出当前可以回放的所有 replay
  while not is_replay_finished(request_status):
    # 筛选出能够在本 time slot 中回放的请求
    request_to_replay = []
    for entry in extracted_har_object['filtered_entries']:
      request_url = entry['request_url']
      if not request_status[request_mapping[request_url]] and \
        is_dependencies_met(entry['dependencies'], request_status):
        request_to_replay.append(request_url)
    replay_log.append(request_to_replay)
    # 把本次回放的请求状态标记为已完成状态
    for request_url in request_to_replay:
      request_status[request_mapping[request_url]] = True

  return replay_log


def is_dependencies_met(dependencies, request_status):
  """
  是否此请求的所有依赖项都已经被满足

  :param dependencies: 此请求的依赖项数组
  :param request_status: 所有请求的状态
  :return: 是否所有依赖项都已经被满足
  """
  for d in dependencies:
    if not request_status[d]:
      return False
  return True


def is_replay_finished(request_status):
  """
  是否已经回放了所有请求

  :param request_status: 请求的状态，True 指已被回放，False 指未被回放
  :return: 是否已经回放了所有请求
  """
  # 默认为已完成状态，如果在遍历过程中遇到尚未回放的请求，则会变成未完成状态
  result = True
  for status in request_status:
    result &= status
  return result


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
  # 把收集到的 rtt 数据更新到数据集中
  for entry in extracted_har_object['filtered_entries']:
    entry['server_delay'] = entry['ttfb'] - ping_result[entry['hostname']]


def main():
  # 读取 JSON 数据
  file_path = 'har-sample.json'
  json_object = read_json_file(file_path)
  print('json file loaded, file path = <%s>' % file_path)

  # 从 HAR 文件中提取所需信息
  extracted_har_object = extract_har_json_object(json_object)
  # 在获取了所有的依赖项之后，就可以按照依赖项顺序回放各请求了
  filtered_requests = extracted_har_object['filtered_entries']

  # 计算服务器生成响应的延迟
  print('retrieving average rtt for all hostname')
  get_hostname_average_rtt(extracted_har_object)

  # 重放请求以获取各项指标
  replay_log = replay(extracted_har_object)
  print('time slot count: %s' % len(replay_log))
  request_count = 1
  for time_slot in range(len(replay_log)):
    print('in time slot: <%3d>, %3d requested replayed' %
          (time_slot + 1, len(replay_log[time_slot])))
    for request in replay_log[time_slot]:
      print('  <%3d>: %s' % (request_count, request))
      request_entry = get_request_by_url(filtered_requests, request)
      method = request_entry['method']
      response_transfer_size = request_entry['response_transfer_size'] / 1e3
      ttfb = request_entry['ttfb']
      hostname = request_entry['hostname']
      server_delay = request_entry['server_delay']
      resource_type = request_entry['resource_type']
      print('    method: <%s>, size: <%4.2f>KB, TTFB: <%3.3f>ms, hostname: <%s>, '
            'server delay: <%3.3f>ms, resource type: <%s>' %
            (method, response_transfer_size, ttfb, hostname, server_delay, resource_type))
      request_count += 1


if __name__ == '__main__':
  main()
