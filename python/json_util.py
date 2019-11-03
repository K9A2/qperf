# coding: utf-8

import json


def read_json_file(file_name):
  """
  读取 JSON 文件

  :param file_name: JSON 文件路径
  :return: JSON 文件包含的数据
  """
  with open(file_name, 'r') as f:
    data = json.load(f)
  return data


def output_to_file(file_name, json_object):
  """
  把 JSON 对象写入文件

  :param file_name: 文件名
  :param json_object: JSON 对象
  """
  with open(file_name, 'w') as f:
    json.dump(json_object, f)
