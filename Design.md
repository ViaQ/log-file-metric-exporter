
### Logwatcher Design

Logwatcher uses Linux inotify[1] to add watches to directories and files generated in "/var/log/pods". Different
set of watch flags are used for directories and files.

Watches for folders:
 - `IN_CREATE`: watch is triggered when a file or direcory is created in this directory.
 - `IN_DELETE`: watch is triggered when a file or direcory is deleted in this direcory.

Watches for files:
 - `IN_MODIFY`: watch is triggered when file is written.
 - `IN_CLOSE_WRITE`: watch is triggered when file is closed for writing.
 - `IN_ONESHOT`: When ONESHOT flag is used with other set of flags, when the condition for watch is fulfilled, 
   and a message is sent, the watch is deleted after sending the message. Application needs to set the watch again
   if needed.

When some activity on directories and files, watches are triggered, and a message is sent on watch file descriptor. 
Since watches on files are not kept permanently and deleted as soon as the watch is triggered because of ONESHOT flag,
messages do not accumulate in the inotify queue. Without ONESHOT flag, with logs continuosely getting writen, queue can
get full very easily and messages start getting dropped.

### Watch Structure


    /var/log/pods/  (IN_CREATE|IN_DELETE)
      │
      │
      ├───── openshift-kube-controller-manager_kube-controller-manager-crc-pbwlw-master-0_738a9f84e9aed99070694fd38123a679/  (IN_CREATE|IN_DELETE)
      │        │
      │        ├────────── cluster-policy-controller/  (IN_CREATE|IN_DELETE)
      │        │            │
      │        │            │       0.log (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
      │        │            │       4.log (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
      │        │            │       5.log (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
      │        │            └────── 6.log (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
      │        │                    7.log (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
      │        │                    8.log (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
      │        │
      │        │
      │        └────────── kube-controller-manager/  (IN_CREATE|IN_DELETE)
      │                     │
      │                     │       0.log  (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
      │                     │       10.log (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
      │                     │       11.log (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
      │                     └────── 12.log (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
      │                             13.log (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
      │                             9.log  (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
      │
      │
      └────── openshift-cluster-version_cluster-version-operator-8b9c98bfd-8mj5d_60452d84-5db1-4e5f-815c-245aaa76cbb9/  (IN_CREATE|IN_DELETE)
                │
                │
                └───────── cluster-version-operator/  (IN_CREATE|IN_DELETE)
                            │
                            │       0.log                    (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
                            │       1.log                    (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
                            │       1.log.20230102-180708    (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
                            │       2.log                    (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
                            │       2.log.20230104-094208.gz (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
                            │       2.log.20230104-182347.gz (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
                            │       2.log.20230105-030537.gz (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
                            └────── 2.log.20230105-114647    (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
                                    3.log                    (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
                                    4.log                    (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
                                    4.log.20230111-190053    (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)
                                    5.log                    (IN_MODIFY|IN_CLOSE_WRITE|IN_ONESHOT)

### Read Loop
A goroutine reads the inotify file descriptor and reads the raw events posted to the inotify file descriptor, and sends an
event to the Event Loop for handeling over a buffered channel.

### Event Loop
A goroutine reads the buffered channel, and processes the NotifyEvent.



### References
 [1] [Filesystem notification, part 2: A deeper investigation of inotify](https://lwn.net/Articles/605128/)

