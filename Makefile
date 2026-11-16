# Native package builds for session-vault-server, in the style of
# github.com/Zagitta/gpu-unlock:
#
#   make dist             versioned source tarball (build/...tar.xz)
#   make arch|deb|rpm     build the package for that family
#   make install          detect the distro from /etc/os-release, then install
#   make install-arch     Arch:      makepkg + pacman -U
#   make install-deb      Debian/ubuntu: dpkg-buildpackage + dpkg -i
#   make install-rpm      Fedora/RHEL:   rpmbuild + dnf install
#   make clean            remove all build artifacts
#
# VERSION defaults to the latest git tag (release-please tags vX.Y.Z);
# override with `make VERSION=x.y.z` to build an untagged tree.

# SVS_NAME (not PKG): some toolchains export PKG in the environment, and
# make gives environment/command-line variables precedence over the Makefile.
SVS_NAME := session-vault-server
VERSION  ?= $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')
DISTDIR  ?= build
TARBALL  := $(abspath $(DISTDIR))/$(SVS_NAME)-$(VERSION).tar.xz
ARCHDIR  := $(abspath $(DISTDIR))/arch
DEBROOT  := $(abspath $(DISTDIR))/deb
RPMDIR   := $(abspath $(DISTDIR))/rpm
DEBS     := $(DEBROOT)/$(SVS_NAME)_$(VERSION)_*.deb
RPMS     := $(RPMDIR)/RPMS/*/$(SVS_NAME)-*.rpm
MAINTAINER ?= Paolo Galeone <nessuno@nerdz.eu>

.PHONY: all dist arch deb rpm version install \
        install-arch install-deb install-rpm clean

all:

version:
	@echo $(VERSION)

# Versioned source tarball from the tracked files; the build source for the
# native packages below.
dist:
	@test -n "$(VERSION)" || { \
		echo "no git tag found; tag the release (vX.Y.Z) or pass VERSION=x.y.z"; \
		exit 1; }
	mkdir -p $(DISTDIR)
	git archive --format=tar --prefix=$(SVS_NAME)-$(VERSION)/ \
		--output=$(DISTDIR)/$(SVS_NAME)-$(VERSION).tar HEAD -- $$(git ls-files)
	xz -9 -f $(DISTDIR)/$(SVS_NAME)-$(VERSION).tar

# --- Arch Linux (makepkg) ------------------------------------------------

arch: dist
	rm -rf $(ARCHDIR)
	mkdir -p $(ARCHDIR)
	sed 's/^pkgver=.*/pkgver=$(VERSION)/' packaging/arch/PKGBUILD > $(ARCHDIR)/PKGBUILD
	cp $(TARBALL) $(ARCHDIR)/
	cd $(ARCHDIR) && makepkg -f

install-arch:
	# makepkg ships with pacman itself; only the Go toolchain is missing by
	# default, and only install it (with sudo) if it actually is.
	@command -v go >/dev/null 2>&1 || sudo pacman -S --noconfirm go
	$(MAKE) arch
	sudo pacman -U --noconfirm $(ARCHDIR)/$(SVS_NAME)-$(VERSION)-1-*.pkg.tar.*

# --- Debian / Ubuntu (dpkg) ---------------------------------------------

deb: dist
	rm -rf $(DEBROOT)
	mkdir -p $(DEBROOT)
	tar -xf $(TARBALL) -C $(DEBROOT)
	mv $(DEBROOT)/$(SVS_NAME)-$(VERSION) $(DEBROOT)/$(SVS_NAME)_$(VERSION)
	cp -r packaging/debian $(DEBROOT)/$(SVS_NAME)_$(VERSION)/debian
	printf '%s (%s) unstable; urgency=low\n\n  * Built from git tag v%s.\n\n -- %s  %s\n' \
		'$(SVS_NAME)' '$(VERSION)' '$(VERSION)' '$(MAINTAINER)' '$(shell LC_ALL=C date -R)' \
		> $(DEBROOT)/$(SVS_NAME)_$(VERSION)/debian/changelog
	dpkg-buildpackage -us -uc -b

install-deb:
	@command -v go >/dev/null 2>&1 && command -v dpkg-buildpackage >/dev/null 2>&1 \
		|| sudo apt-get install -y --no-install-recommends golang-go debhelper dpkg-dev
	$(MAKE) deb
	sudo dpkg -i $(DEBS)

# --- Fedora / RHEL / CentOS (rpmbuild) ------------------------------------

rpm: dist
	rm -rf $(RPMDIR)
	mkdir -p $(RPMDIR)/SOURCES
	cp $(TARBALL) $(RPMDIR)/SOURCES/
	rpmbuild -ba --define "_topdir $(RPMDIR)" --define "version $(VERSION)" \
		packaging/rpm/$(SVS_NAME).spec

install-rpm:
	@command -v go >/dev/null 2>&1 && command -v rpmbuild >/dev/null 2>&1 \
		|| sudo dnf install -y rpm-build golang systemd-rpm-macros
	sudo dnf builddep -y packaging/rpm/$(SVS_NAME).spec
	$(MAKE) rpm
	sudo dnf install -y $(RPMS)

# --- Distro detection ------------------------------------------------------

# Detect the distro family from /etc/os-release and install the native
# package, like `make install` in gpu-unlock.
install:
	@set -eu; \
	if [ -r /etc/os-release ]; then \
		. /etc/os-release; \
	elif [ -r /usr/lib/os-release ]; then \
		. /usr/lib/os-release; \
	fi; \
	for distro in $${ID:-} $${ID_LIKE:-}; do \
		case "$$distro" in \
			arch) target=arch ;; \
			debian|ubuntu) target=deb ;; \
			fedora|rhel|centos) target=rpm ;; \
			*) continue ;; \
		esac; \
		echo "Detected $$distro family; running make install-$$target"; \
		$(MAKE) install-$$target; \
		exit $$?; \
	done; \
	echo "Unsupported distribution: $${ID:-unknown}. Use make install-arch, install-deb, or install-rpm explicitly." >&2; \
	exit 1

clean:
	rm -rf $(DISTDIR)
