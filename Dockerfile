FROM registry.access.redhat.com/ubi8/go-toolset AS builder

WORKDIR /go/src/github.com/ViaQ/logwatcher
COPY . .
USER 0
RUN go mod download
RUN make build

#FROM registry.access.redhat.com/ubi8
FROM centos:centos8
USER 0
RUN sed -i 's/mirrorlist/#mirrorlist/g' /etc/yum.repos.d/CentOS-*
RUN sed -i 's|#baseurl=http://mirror.centos.org|baseurl=http://vault.centos.org|g' /etc/yum.repos.d/CentOS-*
#RUN echo 'kernel.yama.ptrace_scope = 0' >> /etc/sysctl.conf
RUN yum -y install strace
RUN yum -y install trace-cmd
COPY --from=builder /go/src/github.com/ViaQ/logwatcher/bin/logwatcher  /usr/local/bin/
COPY --from=builder /go/src/github.com/ViaQ/logwatcher/hack/list-watches.sh  /usr/local/bin/
CMD ["sh", "-c", "/usr/local/bin/logwatcher", "-watch_dir=/var/log/pods", "-v=0", "-logtostderr=true"]
