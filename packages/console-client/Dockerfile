FROM oven/bun:1.4.3 AS bun
FROM node:24.19.0-bookworm-slim AS build
COPY --from=bun /usr/local/bin/bun /usr/local/bin/bun
WORKDIR /src
RUN corepack enable && corepack prepare pnpm@10.34.5 --activate
COPY package.json pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile --ignore-scripts
COPY . .
RUN bun run build

FROM nginxinc/nginx-unprivileged:1.30.5-alpine
ENV CONSOLE_API_ORIGIN=http://console-server:8090
ENV NGINX_ENVSUBST_FILTER=^CONSOLE_API_ORIGIN$
COPY --from=build /src/dist /usr/share/nginx/html
COPY deployment/default.conf.template /etc/nginx/templates/default.conf.template
EXPOSE 8080
