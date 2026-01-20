package schema

type L2Update struct {
	Exchange   string
	Symbol     string
	Timestamp  int64
	RecvTime   int64
	Seq        int64
	IsSnapshot bool
	Bids       []PriceLevel
	Asks       []PriceLevel
	Checksum   uint32
	DepthCap   int
}

type PriceLevel struct {
	Price string
	Size  string
	Count int64
}

type PriceUpdate struct {
	Exchange  string
	Symbol    string
	Timestamp int64
	RecvTime  int64
	Price     string
	Bid       string
	Ask       string
	Source    string
}
