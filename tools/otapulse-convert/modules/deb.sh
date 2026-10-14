# Copyright 2025 Northern.tech AS
#
#    Licensed under the Apache License, Version 2.0 (the "License");
#    you may not use this file except in compliance with the License.
#    You may obtain a copy of the License at
#
#        http://www.apache.org/licenses/LICENSE-2.0
#
#    Unless required by applicable law or agreed to in writing, software
#    distributed under the License is distributed on an "AS IS" BASIS,
#    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#    See the License for the specific language governing permissions and
#    limitations under the License.

source modules/chroot.sh
source modules/log.sh
source modules/probe.sh

deb_chroot_set_up=0

# Install deb package(s) using a chroot
#
#  $1 - Dest directory
#  $@ - Deb package(s)
#
function deb_install_packages() {
    if [[ $# -lt 2 ]]; then
        log_fatal "deb_install_packages() requires at least 2 arguments"
    fi

    local -r dest_dir="$(pwd)/${1}"
    shift

    local -a deb_packages=()
    for pkg in "$@"; do
        deb_packages+=("$(pwd)/${pkg}")
    done

    (
        # This may be called from a larger script that manages the chroot
        # setup/teardown by using deb_prepare/cleanup or from a place where the
        # chroot is supposed to be set up and torn down for a single package
        # installation.
        if [[ $deb_chroot_set_up -eq 1 ]]; then
            deb_install_packages_in_chroot "$dest_dir" "${deb_packages[@]}"
        else
            run_with_chroot_setup "$dest_dir" deb_install_packages_in_chroot "$dest_dir" "${deb_packages[@]}"
        fi
    )
}

function deb_install_packages_in_chroot() {
    if [[ $# -lt 2 ]]; then
        log_fatal "deb_install_packages_in_chroot() requires at least 2 arguments"
    fi

    local -r dest_dir="${1}"
    shift
    local -a deb_packages=("$@")

    # Copy packages to somewhere reachable from within the chroot.
    local -a tmp_packages=()
    for deb_package in "${deb_packages[@]}"; do
        cp "$deb_package" "$dest_dir/tmp/"
        tmp_packages+=("/tmp/$(basename "$deb_package")")
    done

    local ret=0
    # The soc-ota-agent package reads these on its first installation and writes them into
    # /etc/otapulse/otapulse.conf (empty ones are ignored).
    local dpkg_output
    dpkg_output=$(run_in_chroot_and_log_cmd_with_output_noexit "$dest_dir" "env \
        DEBIAN_FRONTEND=noninteractive \
        OTAPULSE_SERVER_URL=${OTAPULSE_SERVER_URL} \
        OTAPULSE_ROOTFS_PART_A=${root_part_a_device} \
        OTAPULSE_ROOTFS_PART_B=${root_part_b_device} \
        OTAPULSE_DEVICE_TYPE=${OTAPULSE_DEVICE_TYPE} \
        dpkg --install ${tmp_packages[*]}") || ret=$?

    # Clean up copied packages
    for deb_package in "${deb_packages[@]}"; do
        rm -f "$dest_dir/tmp/$(basename "$deb_package")"
    done
    case $ret in
        0)
            # Success
            ;;
        1)
            # Check if this is actually a dependency problem that can be fixed with apt
            if ! echo "$dpkg_output" | grep -qE "(dependency problems|depends on.*not installed)"; then
                log_error "dpkg failed but not due to missing dependencies:"
                log_fatal "$dpkg_output"
            fi

            log_info "Installing dependencies for packages"

            # Try to avoid excessive repository pulling. Only update if older than one day.
            if [ ! -e "$dest_dir/var/cache/apt/pkgcache.bin" ] || [ $(date -r "$dest_dir/var/cache/apt/pkgcache.bin" +%s) -lt $(date -d 'today - 1 day' +%s) ]; then
                if [ ! -e "$dest_dir/var/cache/apt/pkgcache.bin" ]; then
                    # If the package cache didn't originally exist, delete it afterwards so we don't
                    # increase space usage.
                    mkdir -p "$dest_dir/var/cache/apt"
                    touch "$dest_dir/var/cache/apt/otapulse-convert.remove-apt-cache"
                fi

                run_in_chroot_and_log_cmd "$dest_dir" "apt update"
            fi

            # It's ok if dpkg fails, as long as a subsequent "apt install" succeeds. This happens
            # when we try to install a package and its dependencies are not immediately
            # satisfied. Then we need to run apt afterwards to remedy the problem.
            run_in_chroot_and_log_cmd "$dest_dir" "env \
                DEBIAN_FRONTEND=noninteractive \
                OTAPULSE_SERVER_URL=${OTAPULSE_SERVER_URL} \
                OTAPULSE_ROOTFS_PART_A=${root_part_a_device} \
                OTAPULSE_ROOTFS_PART_B=${root_part_b_device} \
                OTAPULSE_DEVICE_TYPE=${OTAPULSE_DEVICE_TYPE} \
                apt --fix-broken --assume-yes install"

            # Verify that all packages were installed. If we install input packages with dependencies not available in the APT repositories,
            # `apt --fix-broken install` will remove the packages and exit with 0
            for deb_package in "${deb_packages[@]}"; do
                local pkg_name="$(dpkg-deb -f "$deb_package" Package)"
                # From dpkg-query man page: db:Status-Status contains the package status word
                local status=$(run_in_chroot_and_log_cmd_with_output "$dest_dir" "dpkg-query -W -f='\${db:Status-Status}' $pkg_name")
                if [[ "$status" != "installed" ]]; then
                    log_error "Package "$deb_package" ("$pkg_name") was not installed. This typically means some of its dependencies are not available in the configured APT repositories."
                    log_fatal "$(dpkg --field "$deb_package")"
                fi
                log_info ""$deb_package" ("$pkg_name") installed successfully"
            done
            ;;
        *)
            # Other return codes are not expected.
            log_fatal "dpkg returned unexpected return code: $ret"
            ;;
    esac
}

#  Do preparations before deb operations in the image
#  $1 - image root directory
function deb_prepare() {
    local -r root_dir="${1}"

    chroot_setup "$root_dir" && deb_chroot_set_up=1
}

#  Install binary files of a deb package into work/deb-packages from a local directory
#  $1 - Directory path
#
function deb_get_and_install_input_package() {
    if ! [[ $# -eq 1 ]]; then
        log_fatal "deb_get_and_install_input_package() requires 1 argument"
    fi

    local -r package_path="$1"
    local -a all_packages=("${package_path}"/*.deb)

    # Check if we have any packages
    if [[ ! -e "${all_packages[0]}" ]]; then
        log_warn "No .deb packages found in ${package_path}"
        return
    fi

    # Only the packages for the architecture of the image (and architecture independent ones)
    # are installed, so one directory can hold the packages of all architectures.
    local -r image_arch="$(probe_debian_arch_name)"
    local -a packages=()
    local pkg pkg_arch
    for pkg in "${all_packages[@]}"; do
        pkg_arch="$(dpkg-deb -f "$pkg" Architecture)"
        if [[ "$pkg_arch" == "$image_arch" || "$pkg_arch" == "all" ]]; then
            packages+=("$pkg")
        else
            log_info "Skipping $(basename "$pkg"): architecture $pkg_arch, the image is $image_arch"
        fi
    done
    if [[ ${#packages[@]} -eq 0 ]]; then
        log_warn "No .deb package for the architecture ${image_arch} in ${package_path}"
        return
    fi

    log_info "Installing ${#packages[@]} package(s) from ${package_path}"

    # Install all packages at once - dpkg handles dependencies automatically
    deb_install_packages "work/rootfs/" "${packages[@]}"
}

#  Install packages of the image's own distribution (tools that the image needs at run time and
#  that the input image may not have, for example the partition tools of the data partition growth).
#  The chroot must be set up (deb_prepare).
#  $@ - package names
function deb_install_distro_packages() {
    if [[ $# -lt 1 ]]; then
        log_fatal "deb_install_distro_packages() requires at least 1 argument"
    fi
    local -r image_root="$(pwd)/work/rootfs"
    if [ ! -e "${image_root}/var/cache/apt/pkgcache.bin" ]; then
        # Do not leave a package cache behind that the input image did not have.
        mkdir -p "${image_root}/var/cache/apt"
        touch "${image_root}/var/cache/apt/otapulse-convert.remove-apt-cache"
    fi
    log_info "Installing from the distribution: $*"
    run_in_chroot_and_log_cmd "work/rootfs/" "env DEBIAN_FRONTEND=noninteractive apt-get update"
    run_in_chroot_and_log_cmd "work/rootfs/" "env DEBIAN_FRONTEND=noninteractive apt-get install --assume-yes --no-install-recommends $*"
}

function deb_cleanup() {
    local -r dest_dir="$(pwd)/${1}"
    if [[ $deb_chroot_set_up -eq 0 ]]; then
        # Already cleaned up, this is not the first call of this function since
        # the last deb_prepare().
        return
    fi

    chroot_teardown "$dest_dir" && deb_chroot_set_up=0
    if [ -e "$dest_dir/var/cache/apt/otapulse-convert.remove-apt-cache" ]; then
        rm -rf "$dest_dir/var/cache/apt"
    fi
}
