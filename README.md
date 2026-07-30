# eqms

[seismometer](https://github.com/njm2360/seismometer/)がUSBシリアルへ出力するNMEAデータを受信して、リアルタイム震度と波形、過去の地震記録をブラウザで見るためのサーバーです

## 使用方法

```sh
make build   # web/ を pnpm build してから go build
EQMS_SERIAL_PORT=/dev/ttyACM0 EQMS_SERIAL_BAUD=115200 ./eqms
```

設定はすべて環境変数で渡す

| 環境変数               | デフォルト       | 説明                                                                   |
| ---------------------- | ---------------- | ---------------------------------------------------------------------- |
| `EQMS_LISTEN`          | `127.0.0.1:8080` | HTTPリッスンアドレス                                                   |
| `EQMS_SERIAL_PORT`     | なし (必須)      | シリアルポート (例 `/dev/ttyACM0`)                                     |
| `EQMS_SERIAL_BAUD`     | なし (必須)      | シリアルボーレート                                                     |
| `EQMS_DB`              | `eqms.db`        | SQLiteデータベースパス                                                 |
| `EQMS_RETENTION`       | `720h`           | 地震記録波形の保持期間。`0` で無期限                                   |
| `EQMS_SIM`             | off              | `1` で実機なしの疑似地震データ生成 (`EQMS_SERIAL_PORT` / `_BAUD` 不要) |
| `EQMS_SERIAL_SILENCE`  | `15s`            | ポートが開いたままこれだけ無音なら切断扱いにして再接続                 |
| `EQMS_MAX_STREAMS`     | `100`            | SSEの同時接続上限。`0` で無制限                                        |
| `EQMS_STREAM_WRITE`    | `10s`            | SSEの1回の書き込みに許す時間。超えた購読者は切断する                   |
| `EQMS_START_INTENSITY` | `0.5`            | 記録を開始する計測震度                                                 |
| `EQMS_END_INTENSITY`   | `0.5`            | これ未満が `EQMS_END_QUIET` 続いたら記録終了                           |
| `EQMS_PREBUFFER`       | `1m`             | 検知時点より前に遡って記録する長さ。最大 `5m`                          |
| `EQMS_END_QUIET`       | `30s`            | 記録終了とみなす静けさの長さ                                           |
| `EQMS_MAX_EVENT`       | `6h`             | 1つの地震記録の最大長。超えたら強制終了する                            |
