# coding:utf-8

import os

from mininet.cli import CLI
from mininet.link import TCLink
from mininet.log import setLogLevel
from mininet.net import Mininet
from mininet.node import CPULimitedHost
from mininet.node import OVSController
from mininet.topo import Topo

# 带宽为 100Mbps
bandwidth = 100
# 单向传播延迟
d = '50ms'
# 丢包率为 1%
l = 0.01

# 程序角色
run_as_server = 's'
run_as_client = 'c'


class Topology(Topo):
  def __init__(self):
    Topo.__init__(self)

    # 一共准备 6 组实验，stream 数量范围为 1-6，数据总量为 60MB，同时进行 3 个 group 以提高资源利用率
    for g in range(0, 5):
      # GroupNumber
      for i in range(1, 7):
        client_name = 'h%d%d%d' % (g, i, 1)
        server_name = 'h%d%d%d' % (g, i, 2)
        switch_name = 's%d%d%d' % (g, i, 1)
        client = self.addHost(client_name)
        server = self.addHost(server_name)
        switch = self.addSwitch(switch_name)

        self.addLink(client, switch, bw=bandwidth, delay=d, loss=l)
        self.addLink(server, switch, bw=bandwidth, delay=d, loss=l)


def main():
  net = Mininet(topo=Topology(), host=CPULimitedHost,
                link=TCLink, controller=OVSController)
  net.start()

  print 'network started'

  # for g in range(0, 5):
  #     # GourpNumber
  #     print 'group %d' % g
  #     for i in range(1, 7):
  #         client_name = 'h%d%d%d' % (g, i, 1)
  #         server_name = 'h%d%d%d' % (g, i, 2)
  #         client = net.get(client_name)
  #         server = net.get(server_name)

  #         command_teamplate = './main -%s -C group-%d-config-%d.json &'

  #         server_command = command_teamplate % (run_as_server, g, i)
  #         print('server_command: %s' % server_command)
  #         server.cmd(server_command)
  #         print(
  #             'group-%d-server-%s dispatched, client will be executed 3s later' % (g, i))

  #         time.sleep(3)

  #         client_command = command_teamplate % (run_as_client, g, i)
  #         print('client_command: %s' % client_command)
  #         client.cmd(client_command)
  #         print(
  #             'group-%d-client-%s dispatched, next evaluation will be executed 3s later' % (g, i))

  print 'all evaluation dispatched'

  CLI(net)
  net.stop()


if __name__ == '__main__':
  os.system('mn -c')
  setLogLevel('info')
  main()
