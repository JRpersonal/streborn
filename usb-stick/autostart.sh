#!/bin/sh
# autostart.sh: DEPRECATED.
#
# Diese Datei stammt aus der Planungsphase. Die produktive Bootstrap
# Kette ist jetzt:
#
#   /mnt/nv/rc.local  (via shelby_local beim Boot)
#       -> /media/sda1/run.sh
#           -> /media/sda1/streborn-armv7l
#
# Installation:
#   sh /media/sda1/install.sh
#
# Diese autostart.sh ist nur noch ein Fallback Eintrittspunkt der run.sh
# aufruft. Kann ohne Verlust entfernt werden sobald die Migration
# abgeschlossen ist.

exec "$(dirname "$0")/run.sh" "$@"
