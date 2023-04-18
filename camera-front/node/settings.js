const setting = {
    apmServerConfig: {
        host: '10.10.103.96',
        port: '2234',
    },
    esServerConfig: {
        host: '10.10.103.96',
        port: '9200',
    },
    profileConfig: {
        host: '10.10.103.149',
        port: '9503',
    },
    ratelimit: {
        windowMs: 10 * 60 * 1000,
        max: 500
    },
    traceFilePath: '/Users/thousand/Downloads/tmp/kindling',
    port: 9900,
};
module.exports = setting;
