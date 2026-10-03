#!/bin/sh
# Set a permissive umask before execing the busy binary. With
# world-writable defaults, anything the agent writes (patches, plans,
# clones in /work) is editable from the host without a chmod pass —
# which matters for samba-backed paths where the samba user needs to
# be able to delete/modify agent output.
umask 0000
exec /usr/local/bin/busy "$@"
