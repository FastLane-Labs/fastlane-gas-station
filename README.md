# Fastlane Gas Station

Upstream go repository for easy and continuous gas charging for various fastlane bundlers.

## Usage

```
go get github.com/FastLane-Labs/fastlane-gas-station
```

```go

gasStation := gasstation.NewGasStation(
    &GasStationConfig{
        pk: gasStationPk,
        ethRpcUrl: "https://mainnet.infura.io/v3/...",
        recheckInterval: 1 * time.Minute,
        metricsEnabled: true,
    }, 
    []*CarConfig{
        &CarConfig{
            addr: carAddr1,
            idealBalance: big.NewInt(1e18),
            toleranceInBips: 100, // 1% = recharge when balance is 1% less than ideal
        },
        &CarConfig{
            addr: carAddr2,
            idealBalance: big.NewInt(2e18),
            toleranceInBips: 1000, // 10% = recharge when balance is 10% less than ideal
        },
    },    
)

gasStation.Start()
```

## Configuration

### Gas Station Configuration

```go
type GasStationConfig struct {
    pk *ecdsa.PrivateKey
    ethRpcUrl string
    recheckInterval time.Duration
}
```

### Car Configuration

```go
type CarConfig struct {
    addr common.Address
    idealBalance *big.Int
    toleranceInBips int64
}
```

### Features

- Configurable charge requirements for each car.
- Batch call to recharge all cars in a single transaction.
- Prometheus metrics for gas station and connected cars.


