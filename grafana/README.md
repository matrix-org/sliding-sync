# Using the Sliding Sync Grafana dashboard

**Set up Prometheus and Grafana.** Out of scope for this readme. Useful documentation about using Grafana with Prometheus: http://docs.grafana.org/features/datasources/prometheus/

**Configure the bind address** for prometheus metrics in sliding sync. For example: `SYNCV3_PROM=2112`. Metrics will be accessible at /metrics at this address.

**Configure a new job in Prometheus** to scrape the sliding sync endpoint.

For example by adding the following job to `prometheus.yml`, if your sliding sync is running locally and you have `SYNCV3_PROM` configured to port 2112:

```
# Sliding Sync
  - job_name: "Sliding Sync"
    static_configs:
      - targets: ["localhost:2112"]
```

**Import the sliding sync dashboard into Grafana.** Download `sliding-sync.json`. Import it to Grafana and select the correct Prometheus datasource. http://docs.grafana.org/reference/export_import/