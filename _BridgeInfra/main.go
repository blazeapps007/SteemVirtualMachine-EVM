package main

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/steemit/steemgosdk/api"
)

func printTransfers(blockNum uint, block interface{}) {
	// Convert the SDK block into JSON
	b, err := json.Marshal(block)
	if err != nil {
		log.Println(err)
		return
	}

	// Decode into a generic map
	var obj map[string]interface{}
	if err := json.Unmarshal(b, &obj); err != nil {
		log.Println(err)
		return
	}

	// Block metadata
	timestamp, _ := obj["timestamp"].(string)
	witness, _ := obj["witness"].(string)

	fmt.Println()
	fmt.Println("====================================================")
	fmt.Printf("Block %d\n", blockNum)
	fmt.Println("====================================================")
	fmt.Printf("Time    : %s\n", timestamp)
	fmt.Printf("Witness : %s\n", witness)
	fmt.Println()

	txs, ok := obj["transactions"].([]interface{})
	if !ok || len(txs) == 0 {
		fmt.Println("No transactions.")
		return
	}

	foundTransfer := false

	for _, t := range txs {

		tx, ok := t.(map[string]interface{})
		if !ok {
			continue
		}

		txid, _ := tx["transaction_id"].(string)

		ops, ok := tx["operations"].([]interface{})
		if !ok {
			continue
		}

		for _, o := range ops {

			op, ok := o.([]interface{})
			if !ok || len(op) != 2 {
				continue
			}

			opType, ok := op[0].(string)
			if !ok || opType != "transfer" {
				continue
			}

			data, ok := op[1].(map[string]interface{})
			if !ok {
				continue
			}

			foundTransfer = true

			fmt.Println("----------------------------------------")
			fmt.Printf("TX ID  : %s\n", txid)
			fmt.Printf("From   : %v\n", data["from"])
			fmt.Printf("To     : %v\n", data["to"])
			fmt.Printf("Amount : %v\n", data["amount"])
			fmt.Printf("Memo   : %v\n", data["memo"])
		}
	}

	if !foundTransfer {
		fmt.Println("No transfer operations.")
	}
}

func main() {

	// Change to your own node if desired
	rpc := "https://api.steemit.com"
	// rpc := "http://127.0.0.1:8090"

	a := api.NewAPI(rpc)

	//------------------------------------------------------------
	// Dynamic Global Properties
	//------------------------------------------------------------

	dgp, err := a.GetDynamicGlobalProperties()
	if err != nil {
		log.Fatal(err)
	}

	head := uint(dgp.HeadBlockNumber)

	fmt.Println("========================================")
	fmt.Println("Dynamic Global Properties")
	fmt.Println("========================================")
	fmt.Printf("Head Block : %d\n", head)
	fmt.Printf("Witness    : %s\n", dgp.CurrentWitness)
	fmt.Printf("Time       : %s\n", dgp.Time)

	//------------------------------------------------------------
	// Specific Block
	//------------------------------------------------------------

	const blockNum uint = 107791543

	block, err := a.GetBlock(blockNum)
	if err != nil {
		log.Fatal(err)
	}

	printTransfers(blockNum, block)

	//------------------------------------------------------------
	// Last 10 Blocks
	//------------------------------------------------------------

	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("Transfers In Last 10 Blocks")
	fmt.Println("========================================")

	from := head - 9
	to := head + 1

	blocks, err := a.GetBlocks(from, to)
	if err != nil {
		log.Fatal(err)
	}

	for _, b := range blocks {

		if b == nil || b.Block == nil {
			continue
		}

		printTransfers(uint(b.BlockNum), b.Block)
	}
}