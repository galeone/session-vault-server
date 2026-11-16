%define debug_package %{nil}

# Version is passed by the top-level Makefile: --define "version x.y.z"
Name:    session-vault-server
Version: %{version}
Release: 1%{?dist}
Summary: Self-hosted store for Claude Code session transcripts
License: MIT
URL: https://github.com/galeone/session-vault-server
Source0: %{name}-%{version}.tar.xz

BuildRequires: golang
BuildRequires: systemd-rpm-macros
Requires: systemd

%description
session-vault-server is a small, self-hosted store for Claude Code
session transcripts. Transcripts are treated as opaque blobs and are
expected to be uploaded end-to-end encrypted (age), so the server never
parses or inspects them. It ships a hardened systemd unit that runs the
server as an ephemeral dedicated user (DynamicUser).

%build
CGO_ENABLED=0 go build -trimpath \
	-ldflags "-s -w -X main.version=%{version}" \
	-o %{name} .

%install
install -Dm755 %{name} %{buildroot}%{_bindir}/%{name}
install -Dm644 misc/%{name}.service %{buildroot}%{_unitdir}/%{name}.service
install -d -m700 %{buildroot}%{_sysconfdir}/%{name}

%post
# Create the configuration directory, then enable the unit.
[ -d %{_sysconfdir}/%{name} ] || install -d -m 0700 %{_sysconfdir}/%{name}
%systemd_post %{name}.service

%preun
%systemd_preun %{name}.service

%postun
%systemd_postun %{name}.service

%files
%{_bindir}/%{name}
%{_unitdir}/%{name}.service
%dir %attr(0700, root, root) %{_sysconfdir}/%{name}

%changelog
# Managed by release-please (CHANGELOG.md); kept intentionally minimal.
