# -*- mode: ruby -*-
# vi: set ft=ruby :

# All Vagrant configuration is done below. The "2" in Vagrant.configure
# configures the configuration version (we support older styles for
# backwards compatibility). Please don't change it unless you know what
# you're doing.
Vagrant.configure("2") do |config|
  # The most common configuration options are documented and commented below.
  # For a complete reference, please see the online documentation at
  # https://docs.vagrantup.com.

  # Every Vagrant development environment requires a box. You can search for
  # boxes at https://vagrantcloud.com/search.
  config.vm.box = "archlinux/archlinux"

  # Share an additional folder to the guest VM. The first argument is
  # the path on the host to the actual folder. The second argument is
  # the path on the guest to mount the folder. And the optional third
  # argument is a set of non-required options.
  config.vm.synced_folder './', '/project', type: '9p'

  config.vm.provider :libvirt do |vm|
    vm.cpus = 32
    vm.memory = "4096"
  end

  config.vm.provision "shell", inline: <<-SHELL
    pacman -Sy wget
    wget -O /tmp/go.tar.gz https://dl.google.com/go/go1.14rc1.linux-amd64.tar.gz
    tar -C /usr/local -xzf /tmp/go.tar.gz
    echo "PATH=$PATH:/usr/local/go/bin" >> /etc/profile
  SHELL
end
