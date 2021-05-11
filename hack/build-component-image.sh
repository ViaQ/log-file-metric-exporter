#! /bin/bash

set -euo pipefail

dir=$1
fullimagename=$2

tag_prefix="${OS_IMAGE_PREFIX:-"openshift/origin-"}"


dockerfile=Dockerfile

dfpath=${dir}/${dockerfile}

echo "----------------------------------------------------------------------------------------------------------------"
echo "-                                                                                                              -"
echo "Building image $dir - this may take a few minutes until you see any output..."
echo "-                                                                                                              -"
echo "----------------------------------------------------------------------------------------------------------------"
buildargs=""

podman build $buildargs -f $dfpath -t "$fullimagename" $dir
