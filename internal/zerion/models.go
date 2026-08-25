package zerion

// Portfolio is the subset of a wallet portfolio we expose locally.
type Portfolio struct {
	Total     float64
	ChangeAbs float64
	ChangePct float64
	ByType    map[string]float64
	ByChain   map[string]float64
}

// TxQuery is one transactions request. RelPath, when set, is a validated
// relative path+query from links.next and replaces first-page parameters.
type TxQuery struct {
	Address        string
	Currency       string
	PageSize       int
	ChainIDs       []string
	OperationTypes []string
	RelPath        string
}

// TxPage is one Zerion transaction page.
type TxPage struct {
	Items []Tx
	Next  string // relative path+query, or empty
}

// Tx is a normalized transaction.
type Tx struct {
	ID            string
	Hash          string
	Chain         string
	OperationType string
	Status        string
	MinedAt       string
	From          string
	To            string
	FeeValue      *float64
	Transfers     []Transfer
}

// Transfer is a value movement inside a transaction.
type Transfer struct {
	Direction string
	Symbol    string
	Amount    float64
	Value     float64
}

type portfolioResponse struct {
	Data *struct {
		Attributes *struct {
			Total struct {
				Positions float64 `json:"positions"`
			} `json:"total"`
			Changes struct {
				Absolute1d float64 `json:"absolute_1d"`
				Percent1d  float64 `json:"percent_1d"`
			} `json:"changes"`
			ByType  map[string]float64 `json:"positions_distribution_by_type"`
			ByChain map[string]float64 `json:"positions_distribution_by_chain"`
		} `json:"attributes"`
	} `json:"data"`
}

type txResponse struct {
	Links struct {
		Next string `json:"next"`
	} `json:"links"`
	Data []txItem `json:"data"`
}

type txItem struct {
	ID         string `json:"id"`
	Attributes struct {
		OperationType string `json:"operation_type"`
		Hash          string `json:"hash"`
		MinedAt       string `json:"mined_at"`
		SentFrom      string `json:"sent_from"`
		SentTo        string `json:"sent_to"`
		Status        string `json:"status"`
		Fee           *struct {
			Value *float64 `json:"value"`
		} `json:"fee"`
		Transfers []struct {
			Direction string   `json:"direction"`
			Value     *float64 `json:"value"`
			Quantity  struct {
				Float float64 `json:"float"`
			} `json:"quantity"`
			FungibleInfo *struct {
				Symbol string `json:"symbol"`
			} `json:"fungible_info"`
		} `json:"transfers"`
	} `json:"attributes"`
	Relationships struct {
		Chain struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		} `json:"chain"`
	} `json:"relationships"`
}

func mapTx(item txItem) Tx {
	tx := Tx{
		ID:            item.ID,
		Hash:          item.Attributes.Hash,
		Chain:         item.Relationships.Chain.Data.ID,
		OperationType: item.Attributes.OperationType,
		Status:        item.Attributes.Status,
		MinedAt:       item.Attributes.MinedAt,
		From:          item.Attributes.SentFrom,
		To:            item.Attributes.SentTo,
		Transfers:     make([]Transfer, 0, len(item.Attributes.Transfers)),
	}
	if item.Attributes.Fee != nil {
		tx.FeeValue = item.Attributes.Fee.Value
	}
	for _, t := range item.Attributes.Transfers {
		tr := Transfer{
			Direction: t.Direction,
			Amount:    t.Quantity.Float,
		}
		if t.FungibleInfo != nil {
			tr.Symbol = t.FungibleInfo.Symbol
		}
		if t.Value != nil {
			tr.Value = *t.Value
		}
		tx.Transfers = append(tx.Transfers, tr)
	}
	return tx
}
