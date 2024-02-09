# Copyright 2025 Wong Hoi Sing Edison <hswong3i@pantarei-design.com>
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

%global debug_package %{nil}

%global source_date_epoch_from_changelog 0

Name: containers-common
Epoch: 100
Version: 0.62.0
Release: 1%{?dist}
Summary: Configuration files common to github.com/containers
License: Apache-2.0
URL: https://github.com/containers/common/tags
Source0: %{name}_%{version}.orig.tar.gz
BuildArch: noarch
%if 0%{?rhel} == 7
BuildRequires: devtoolset-11
BuildRequires: devtoolset-11-gcc
BuildRequires: devtoolset-11-gcc-c++
BuildRequires: devtoolset-11-libatomic-devel
%endif
BuildRequires: golang-1.24
Requires: oci-runtime

%description
Configuration files and manpages shared by tools that are based on the
github.com/containers libraries, such as Buildah, CRI-O, Podman and
Skopeo.

%prep
%autosetup -T -c -n %{name}_%{version}-%{release}
tar -zx -f %{S:0} --strip-components=1 -C .

%build
%if 0%{?rhel} == 7
. /opt/rh/devtoolset-11/enable
%endif
set -ex && \
    export CGO_ENABLED=1 && \
    go run \
        -mod vendor -buildmode pie -v \
        -ldflags "-s -w" \
        ./cmd/seccomp/generate.go

%install
install -Dpm755 -d %{buildroot}%{_datadir}/containers
install -Dpm755 -d %{buildroot}%{_datadir}/containers/systemd
install -Dpm755 -d %{buildroot}%{_sharedstatedir}/containers
install -Dpm755 -d %{buildroot}%{_sharedstatedir}/containers/sigstore
install -Dpm755 -d %{buildroot}%{_sharedstatedir}/containers/storage
install -Dpm755 -d %{buildroot}%{_sharedstatedir}/containers/storage/overlay-images
install -Dpm755 -d %{buildroot}%{_sharedstatedir}/containers/storage/overlay-layers
install -Dpm755 -d %{buildroot}%{_sysconfdir}/containers
install -Dpm755 -d %{buildroot}%{_sysconfdir}/containers/certs.d
install -Dpm755 -d %{buildroot}%{_sysconfdir}/containers/networks
install -Dpm755 -d %{buildroot}%{_sysconfdir}/containers/oci
install -Dpm755 -d %{buildroot}%{_sysconfdir}/containers/oci/hooks.d
install -Dpm755 -d %{buildroot}%{_sysconfdir}/containers/registries.conf.d
install -Dpm755 -d %{buildroot}%{_sysconfdir}/containers/registries.d
install -Dpm755 -d %{buildroot}%{_sysconfdir}/containers/systemd
install -Dpm644 -t %{buildroot}%{_sysconfdir}/containers/ registries.conf
install -Dpm644 -t %{buildroot}%{_sysconfdir}/containers/ tests/policy.json
install -Dpm644 -t %{buildroot}%{_datadir}/containers/ pkg/config/containers.conf
install -Dpm644 -t %{buildroot}%{_datadir}/containers/ pkg/seccomp/seccomp.json

%files
%license LICENSE
%dir %{_datadir}/containers
%dir %{_datadir}/containers/systemd
%dir %{_sharedstatedir}/containers
%dir %{_sharedstatedir}/containers/sigstore
%dir %{_sharedstatedir}/containers/storage
%dir %{_sharedstatedir}/containers/storage/overlay-images
%dir %{_sharedstatedir}/containers/storage/overlay-layers
%dir %{_sysconfdir}/containers
%dir %{_sysconfdir}/containers/certs.d
%dir %{_sysconfdir}/containers/networks
%dir %{_sysconfdir}/containers/oci
%dir %{_sysconfdir}/containers/oci/hooks.d
%dir %{_sysconfdir}/containers/registries.conf.d
%dir %{_sysconfdir}/containers/registries.d
%dir %{_sysconfdir}/containers/systemd
%{_sysconfdir}/containers/policy.json
%{_sysconfdir}/containers/registries.conf
%{_datadir}/containers/containers.conf
%{_datadir}/containers/seccomp.json

%changelog
