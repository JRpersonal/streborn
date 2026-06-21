#!/bin/sh
# autostart.sh: DEPRECATED.
#
# This file dates from the planning phase. The production bootstrap
# chain is now:
#
#   /mnt/nv/rc.local  (via shelby_local beim Boot)
#       -> /media/sda1/run.sh
#           -> /media/sda1/streborn-armv7l
#
# Installation:
#   sh /media/sda1/install.sh
#
# This autostart.sh is now only a fallback entry point that calls
# run.sh. It can be removed without loss once the migration is
# complete.

exec "$(dirname "$0")/run.sh" "$@"
