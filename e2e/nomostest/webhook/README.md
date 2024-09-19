# creating tls key and cert
```
openssl req -nodes -x509 -sha256 -newkey rsa:4096 \
-keyout tls.key \
-out tls.crt \
-days 356 \
-subj "/C=NL/ST=Zuid Holland/L=Rotterdam/O=ACME Corp/OU=IT Dept/CN=webhook-service.oci-webhook.svc"  \
-addext "subjectAltName = DNS:webhook-service,DNS:webhook-service.oci-webhook.svc,DNS:webhook-service.oci-webhook" 
```