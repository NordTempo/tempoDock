# A tiny web app with psql in its image, for pre-deploy migrations and
# one-off commands: busybox httpd serves "web $APP_VERSION" on 8080.
FROM postgres:17-alpine
RUN apk add --no-cache busybox-extras
CMD ["sh", "-c", "mkdir -p /tmp/www && echo \"web ${APP_VERSION:-v0}\" > /tmp/www/index.html && exec httpd -f -p 8080 -h /tmp/www"]
