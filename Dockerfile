FROM scratch

COPY pod-cidr-overlap-killer /pod-cidr-overlap-killer

ENTRYPOINT ["/pod-cidr-overlap-killer"]
