# hapcon

DualSense コントローラのボタン・方向キー・スティック操作時にハプティックフィードバック（振動）を発生させるツール。

evdev で入力を読み、hidraw で BT HID 出力レポートを直接送信してモーターを制御する。外部依存なし。

## 要件

- Linux (evdev + hidraw)
- Go 1.21+
- DualSense Wireless Controller (Bluetooth)
- `/dev/input/event*` と `/dev/hidraw*` への読み書き権限 (root or udev rule)

## ビルド

```sh
go build -o hapcon .
```

## 使い方

```sh
# DualSense を自動検出して起動
sudo ./hapcon

# verbose モード (入力名を表示)
sudo ./hapcon -v
```

| フラグ | 説明 |
|--------|------|
| `-v` | 入力検知時にボタン/軸名を表示 |

## ハプティックマッピング

| 入力 | right (高周波) | left (低周波) | 持続時間 |
|------|---------------|--------------|---------|
| Cross, Circle, Triangle, Square, Create, Options | 200 | 30 | 15ms |
| L1, R1 | 220 | 50 | 20ms |
| L2, R2 | 255 | 100 | 25ms |
| L3, R3, PS | 150 | 0 | 10ms |
| D-pad, スティック | 220 | 40 | 35ms |

## カスタマイズ

`main.go` の `defaultProfile()` を編集してボタンごとの振動パラメータを調整できる。

- `right`: 高周波モーター強度 (0–255)
- `left`: 低周波モーター強度 (0–255)
- `duration`: パルス持続時間 (ms)

## udev ルール (任意)

root なしで実行するには:

```sh
# /etc/udev/rules.d/99-dualsense.rules
SUBSYSTEM=="input", ATTRS{idVendor}=="054c", ATTRS{idProduct}=="0ce6", MODE="0666"
SUBSYSTEM=="hidraw", ATTRS{idVendor}=="054c", ATTRS{idProduct}=="0ce6", MODE="0666"
```

```sh
sudo udevadm control --reload-rules && sudo udevadm trigger
```

## 仕組み

1. `/dev/input/event*` から DualSense を自動検出（evdev + hidraw）
2. evdev でボタン・軸イベントを監視
3. 入力検知時に hidraw 経由で BT HID 出力レポートを送信し、モーターを直接駆動
4. パルスループが ON→sleep→OFF を同期的に実行し、各パルスを確実に完了させる
