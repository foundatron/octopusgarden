# Sensor Telemetry Ingest

gRPC service for IoT sensor data collection and aggregation.

## Proto Service: TelemetryService

```protobuf
service TelemetryService {
  rpc RegisterSensor(RegisterSensorRequest) returns (Sensor);
  rpc RecordReading(RecordReadingRequest) returns (RecordReadingResponse);
  rpc StreamUpload(stream Reading) returns (UploadSummary);
  rpc WatchSensor(WatchRequest) returns (stream Reading);
  rpc GetStats(GetStatsRequest) returns (StatsResponse);
}
```

### RegisterSensor (unary)

- Input: name, type (temperature|humidity|pressure), location
- Output: sensor with generated id
- Rejects duplicate sensor names with an ALREADY_EXISTS error

### RecordReading (unary)

- Input: sensor_id, value, unit, timestamp
- Output: ack with reading id
- Rejects readings for unknown sensors with NOT_FOUND
- Rejects negative values with INVALID_ARGUMENT

### StreamUpload (client-streaming)

- Client sends stream of Reading messages
- Server returns UploadSummary with count of accepted/rejected readings
- Readings for unknown sensors are counted as rejected (not fatal)

### WatchSensor (server-streaming)

- Input: sensor_id
- Server streams new readings as they arrive in real-time
- Stream stays open until client cancels
- Returns NOT_FOUND if sensor_id is unknown

### GetStats (unary)

- Input: sensor_id
- Output: min, max, avg, count over all readings for that sensor
- Returns zero values if no readings exist
- Returns NOT_FOUND if sensor_id is unknown

## Data Model

- **Sensor**: `id` (string, generated), `name`, `type` (temperature|humidity|pressure), `location`
- **Reading**: `sensor_id`, `value` (float64), `unit`, `timestamp`
- **Stats**: `sensor_id`, `min`, `max`, `avg`, `count`

## Requirements

- Server reflection MUST be enabled
- All state in-memory (no database)
- gRPC server on port 50051
- Any programming language

## Build Notes

- If using Go, the base Docker image MUST be `golang:1.24` or later (gRPC module requires Go 1.24+)
- The Dockerfile must install `protoc` (protobuf compiler) and ensure well-known types are available
  (e.g. `protobuf-dev` or `protobuf` package that provides `/usr/include/google/protobuf/*.proto`)
- Pass `-I /usr/include` to `protoc` so imports like `google/protobuf/timestamp.proto` resolve
  correctly
