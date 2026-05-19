#!/usr/bin/env python3
"""
Remote write utility for generating test metrics.
Supports data metrics and info metrics for join testing.
"""

import argparse
import requests
import snappy
import time
import struct
import sys


def encode_varint(value):
    bits = value & 0x7f
    value >>= 7
    result = b''
    while value:
        result += bytes([0x80 | bits])
        bits = value & 0x7f
        value >>= 7
    result += bytes([bits])
    return result


def encode_string(field_num, s):
    tag = (field_num << 3) | 2
    encoded = s.encode('utf-8')
    return encode_varint(tag) + encode_varint(len(encoded)) + encoded


def encode_label(name, value):
    content = encode_string(1, name) + encode_string(2, value)
    return content


def encode_sample(timestamp_ms, value):
    content = bytes([0x09]) + struct.pack('<d', value)
    content += bytes([0x10]) + encode_varint(timestamp_ms)
    return content


def encode_timeseries(labels, samples):
    content = b''
    for name, value in labels:
        label_bytes = encode_label(name, value)
        content += bytes([0x0a]) + encode_varint(len(label_bytes)) + label_bytes
    for ts, val in samples:
        sample_bytes = encode_sample(ts, val)
        content += bytes([0x12]) + encode_varint(len(sample_bytes)) + sample_bytes
    return content


def encode_write_request(timeseries_list):
    content = b''
    for ts_bytes in timeseries_list:
        content += bytes([0x0a]) + encode_varint(len(ts_bytes)) + ts_bytes
    return content


def send_batch(url, timeseries_list):
    write_req = encode_write_request(timeseries_list)
    compressed = snappy.compress(write_req)
    resp = requests.post(
        url + '/api/v1/write',
        data=compressed,
        headers={
            'Content-Type': 'application/x-protobuf',
            'Content-Encoding': 'snappy',
            'X-Prometheus-Remote-Write-Version': '0.1.0'
        }
    )
    if resp.status_code >= 400:
        print(f"Error: {resp.status_code} {resp.text}", file=sys.stderr)
        return False
    return True


def generate_data_metric(url, num_series, duration_sec, interval_sec=60, metric_name="data_metric", batch_size=500):
    """Generate a high-cardinality data metric."""
    now = int(time.time() * 1000)
    start = now - (duration_sec * 1000)

    print(f"Generating {num_series} {metric_name} series...")
    batch = []
    total = 0

    for i in range(num_series):
        instance = f"instance_{i}"
        labels = [("__name__", metric_name), ("instance", instance), ("job", "test")]
        samples = [(t, float(i % 100)) for t in range(start, now, interval_sec * 1000)]
        batch.append(encode_timeseries(labels, samples))

        if len(batch) >= batch_size:
            if send_batch(url, batch):
                total += len(batch)
                print(f"\r  {total}/{num_series}", end="", flush=True)
            batch = []

    if batch:
        if send_batch(url, batch):
            total += len(batch)

    print(f"\n  Done: {total} series")
    return total


def generate_info_metric(url, num_series, duration_sec, selectivity=0.01,
                         metric_name="instance_info", join_label="instance",
                         filter_label="region", filter_value_match="region_a",
                         filter_value_other="region_b", interval_sec=60, batch_size=500):
    """Generate an info metric with selective filter labels."""
    now = int(time.time() * 1000)
    start = now - (duration_sec * 1000)
    num_matching = max(1, int(num_series * selectivity))

    print(f"Generating {num_series} {metric_name} series...")
    print(f"  {num_matching} ({selectivity*100}%) will have {filter_label}={filter_value_match}")

    batch = []
    total = 0

    for i in range(num_series):
        instance = f"instance_{i}"
        filter_val = filter_value_match if i < num_matching else filter_value_other
        labels = [
            ("__name__", metric_name),
            (join_label, instance),
            (filter_label, filter_val)
        ]
        samples = [(t, 1.0) for t in range(start, now, interval_sec * 1000)]
        batch.append(encode_timeseries(labels, samples))

        if len(batch) >= batch_size:
            if send_batch(url, batch):
                total += len(batch)
                print(f"\r  {total}/{num_series}", end="", flush=True)
            batch = []

    if batch:
        if send_batch(url, batch):
            total += len(batch)

    print(f"\n  Done: {total} series")
    return total


def main():
    parser = argparse.ArgumentParser(description='Generate test metrics via remote write')
    parser.add_argument('--url', default='http://localhost:19090', help='Prometheus URL')
    parser.add_argument('--series', type=int, default=10000, help='Number of series')
    parser.add_argument('--duration', type=int, default=600, help='Duration in seconds')
    parser.add_argument('--interval', type=int, default=60, help='Sample interval in seconds')
    parser.add_argument('--type', choices=['data', 'info', 'both'], default='both', help='Metric type')
    parser.add_argument('--selectivity', type=float, default=0.01, help='Info metric selectivity (0-1)')
    parser.add_argument('--batch-size', type=int, default=500, help='Batch size for remote write')

    args = parser.parse_args()

    if args.type in ['data', 'both']:
        generate_data_metric(args.url, args.series, args.duration, args.interval, batch_size=args.batch_size)

    if args.type in ['info', 'both']:
        generate_info_metric(args.url, args.series, args.duration, args.selectivity,
                            interval_sec=args.interval, batch_size=args.batch_size)


if __name__ == '__main__':
    main()
