FROM --platform=$BUILDPLATFORM node:22-alpine AS frontend-builder
WORKDIR /app
COPY frontend/package.json frontend/package-lock.json* frontend/.npmrc ./
RUN npm ci
COPY frontend/ ./
RUN npm run generate

FROM nginx:1.27-alpine
RUN rm -f /etc/nginx/conf.d/default.conf
COPY nginx/nginx.conf /etc/nginx/nginx.conf
COPY nginx/proxy_params.conf /etc/nginx/includes/proxy_params.conf
COPY --chmod=755 nginx/15-configure-listener.sh /docker-entrypoint.d/15-configure-listener.sh
COPY nginx/default.conf.template /etc/nginx/templates/default.conf.template
COPY --from=frontend-builder /app/.output/public /usr/share/nginx/html

ENV BACKEND_UPSTREAM=backend:8080
ENV NGINX_ENVSUBST_FILTER=^BACKEND_UPSTREAM$

EXPOSE 80
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8081/healthz > /dev/null || exit 1
