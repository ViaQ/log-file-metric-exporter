

#### Watch descriptor reused by inotify

eventLoop:

 1. program adds a watch on a file, gets a watch fd (say `a0`)
 2. file gets modified, program gets an event on watch fd `a0`
 3. (since `IN_ONESHOT` was used, the watch is auto deleted by inotify now.)
 4. program handles the modify (`0x2`) event and does its logic
 5. program adds a watch again on the same file, gets a new watch fd (say `a1`) (this step is same as step 1.)

While testing it is observed that after some iterations of the above loop, the watch fd returned in the step 5 is same 
as one returned in step 1. Once this happens, there are no more events received for file changes. the loop essentially halts.

#### Workaround
Save the watch desctiptor for files, and compare the new watch descriptor with earlier one, if same add watch again will the returned watch descriptor is different.
https://github.com/vimalk78/logwatcher/blob/main/internal/inotify/watch.go#L49-L64
