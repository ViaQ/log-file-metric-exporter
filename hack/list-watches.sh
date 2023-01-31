inotify_file=$(find /proc/1/fd -lname anon_inode:inotify)
inotify_fd=$(basename $inotify_file)
while true; do cat /proc/1/fdinfo/$inotify_fd;echo ""; num=$(cat /proc/1/fdinfo/$inotify_fd| grep ^inotify | wc -l);echo "Total watches: $num"; sleep 1; done

