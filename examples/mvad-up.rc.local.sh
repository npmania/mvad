# Add to /etc/rc.local before "exit 0":
nohup /usr/bin/mvad up se-sto-wg-001 >/var/log/mvad-up.log 2>&1 &
