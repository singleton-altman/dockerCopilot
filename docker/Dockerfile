# 使用 Alpine 作为基础镜像
FROM alpine
LABEL authors="onlyLTY"
WORKDIR /app

ARG TARGETPLATFORM

COPY dc-back/dist/$TARGETPLATFORM/dockerCopilot ./dockerCopilot
COPY dc-back/etc/. ./etc
COPY start.sh ./start.sh

# docker-cli + compose 插件：内置 compose 执行器通过挂载的 docker.sock 执行任务
RUN apk add --no-cache tzdata docker-cli docker-cli-compose \
    && chmod +x start.sh \
    && chmod +x dockerCopilot

# 设置环境变量
ENV secretKey="" \
    DOCKER_HOST="unix:///var/run/docker.sock" \
    BACKUP_DIR="/data/backups" \
    TZ="Asia/Shanghai"

VOLUME ["/data"]
# 设置启动命令
CMD ["./start.sh"]