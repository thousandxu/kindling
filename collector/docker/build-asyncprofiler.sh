ASYNC_PROFILER=async-profiler-2.0.1-linux-x64.tar.gz
KINDLING_JAVA=kindling-java-2.0.1.tar.gz
APM_ALL=apm-all-3.1.0.jar

SCRIPT_DIR="$(cd "$(dirname "$SCRIPT_BIN")" > /dev/null 2>&1; pwd -P)"
if [ ! -d "$SCRIPT_DIR/async-profiler" ];then
    curl -O https://k8s-bpf-probes-public.oss-cn-hangzhou.aliyuncs.com/$ASYNC_PROFILER
    tar -zvxf $ASYNC_PROFILER
    rm -f $ASYNC_PROFILER
fi

if [ ! -d "$SCRIPT_DIR/async-profiler/agent/kindling-java" ];then
    curl -O https://k8s-bpf-probes-public.oss-cn-hangzhou.aliyuncs.com/$KINDLING_JAVA
    tar -zxvf $KINDLING_JAVA
    mkdir -p $SCRIPT_DIR/async-profiler/agent
    mv kindling-java $SCRIPT_DIR/async-profiler/agent/
    rm -f $KINDLING_JAVA
fi

if [ ! -f "$SCRIPT_DIR/async-profiler/agent/apm-all/apm-all.jar" ];then
    mkdir -p $SCRIPT_DIR/async-profiler/agent/apm-all
    cd $SCRIPT_DIR/async-profiler/agent/apm-all
    curl -o apm-all.jar https://k8s-bpf-probes-public.oss-cn-hangzhou.aliyuncs.com/$APM_ALL
fi