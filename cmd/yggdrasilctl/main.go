package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/yggdrasil-network/yggdrasil-go/src/version"
)

type admin_info map[string]interface{}

func main() {
	// makes sure we can use defer and still return an error code to the OS
	os.Exit(run())
}

func run() int {
	logbuffer := &bytes.Buffer{}
	logger := log.New(logbuffer, "", log.Flags())

	defer func() int {
		if r := recover(); r != nil {
			logger.Println("Fatal error:", r)
			fmt.Print(logbuffer)
			return 1
		}
		return 0
	}()

	cmdLineEnv := newCmdLineEnv()
	cmdLineEnv.parseFlagsAndArgs()

	if cmdLineEnv.ver {
		fmt.Println("Build name:", version.BuildName())
		fmt.Println("Build version:", version.BuildVersion())
		fmt.Println("To get the version number of the running Yggdrasil node, run", os.Args[0], "getSelf")
		return 0
	}

	if len(cmdLineEnv.args) == 0 {
		flag.Usage()
		return 0
	}

	cmdLineEnv.setEndpoint(logger)

	conn := connect(cmdLineEnv.endpoint, logger)
	logger.Println("Connected")
	defer conn.Close()

	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)
	send := make(admin_info)
	recv := make(admin_info)

	for c, a := range cmdLineEnv.args {
		if c == 0 {
			if strings.HasPrefix(a, "-") {
				logger.Printf("Ignoring flag %s as it should be specified before other parameters\n", a)
				continue
			}
			logger.Printf("Sending request: %v\n", a)
			send["request"] = a
			continue
		}
		tokens := strings.Split(a, "=")
		if len(tokens) == 1 {
			send[tokens[0]] = true
		} else if len(tokens) > 2 {
			send[tokens[0]] = strings.Join(tokens[1:], "=")
		} else if len(tokens) == 2 {
			if i, err := strconv.Atoi(tokens[1]); err == nil {
				logger.Printf("Sending parameter %s: %d\n", tokens[0], i)
				send[tokens[0]] = i
			} else {
				switch strings.ToLower(tokens[1]) {
				case "true":
					send[tokens[0]] = true
				case "false":
					send[tokens[0]] = false
				default:
					send[tokens[0]] = tokens[1]
				}
				logger.Printf("Sending parameter %s: %v\n", tokens[0], send[tokens[0]])
			}
		}
	}

	if err := encoder.Encode(&send); err != nil {
		panic(err)
	}

	logger.Printf("Request sent")

	if err := decoder.Decode(&recv); err == nil {
		logger.Printf("Response received")
		if recv["status"] == "error" {
			if err, ok := recv["error"]; ok {
				fmt.Println("Admin socket returned an error:", err)
			} else {
				fmt.Println("Admin socket returned an error but didn't specify any error text")
			}
			return 1
		}
		if _, ok := recv["request"]; !ok {
			fmt.Println("Missing request in response (malformed response?)")
			return 1
		}
		if _, ok := recv["response"]; !ok {
			fmt.Println("Missing response body (malformed response?)")
			return 1
		}
		res := recv["response"].(map[string]interface{})

		if cmdLineEnv.injson {
			if json, err := json.MarshalIndent(res, "", "  "); err == nil {
				fmt.Println(string(json))
			}
			return 0
		}

		handleAll(recv, cmdLineEnv.verbose)
	} else {
		logger.Println("Error receiving response:", err)
	}

	if v, ok := recv["status"]; ok && v != "success" {
		return 1
	}

	return 0
}

func connect(endpoint string, logger *log.Logger) net.Conn {
	var conn net.Conn

	u, err := url.Parse(endpoint)

	if err == nil {
		switch strings.ToLower(u.Scheme) {
		case "unix":
			logger.Println("Connecting to UNIX socket", endpoint[7:])
			conn, err = net.Dial("unix", endpoint[7:])
		case "tcp":
			logger.Println("Connecting to TCP socket", u.Host)
			conn, err = net.Dial("tcp", u.Host)
		default:
			logger.Println("Unknown protocol or malformed address - check your endpoint")
			err = errors.New("protocol not supported")
		}
	} else {
		logger.Println("Connecting to TCP socket", u.Host)
		conn, err = net.Dial("tcp", endpoint)
	}

	if err != nil {
		panic(err)
	}

	return conn
}

func handleAll(recv map[string]interface{}, verbose bool) {
	req := recv["request"].(map[string]interface{})
	res := recv["response"].(map[string]interface{})

	switch strings.ToLower(req["request"].(string)) {
	case "dot":
		handleDot(res)
	case "list", "getpeers", "getswitchpeers", "getdht", "getsessions", "dhtping":
		handleVariousInfo(res, verbose)
	case "gettuntap", "settuntap":
		handleGetAndSetTunTap(res)
	case "getself":
		handleGetSelf(res, verbose)
	case "getswitchqueues":
		handleGetSwitchQueues(res)
	case "addpeer", "removepeer", "addallowedencryptionpublickey", "removeallowedencryptionpublickey", "addsourcesubnet", "addroute", "removesourcesubnet", "removeroute":
		handleAddsAndRemoves(res)
	case "getallowedencryptionpublickeys":
		handleGetAllowedEncryptionPublicKeys(res)
	case "getmulticastinterfaces":
		handleGetMulticastInterfaces(res)
	case "getsourcesubnets":
		handleGetSourceSubnets(res)
	case "getroutes":
		handleGetRoutes(res)
	case "settunnelrouting":
		fallthrough
	case "gettunnelrouting":
		handleGetTunnelRouting(res)
	default:
		if json, err := json.MarshalIndent(recv["response"], "", "  "); err == nil {
			fmt.Println(string(json))
		}
	}
}

func handleDot(res map[string]interface{}) {
	fmt.Println(res["dot"])
}

func handleVariousInfo(res map[string]interface{}, verbose bool) {
	maxWidths := make(map[string]int)
	var keyOrder []string
	keysOrdered := false

	for _, tlv := range res {
		for slk, slv := range tlv.(map[string]interface{}) {
			if !keysOrdered {
				for k := range slv.(map[string]interface{}) {
					if !verbose {
						if k == "box_pub_key" || k == "box_sig_key" || k == "nodeinfo" || k == "was_mtu_fixed" {
							continue
						}
					}
					keyOrder = append(keyOrder, fmt.Sprint(k))
				}
				sort.Strings(keyOrder)
				keysOrdered = true
			}
			for k, v := range slv.(map[string]interface{}) {
				if len(fmt.Sprint(slk)) > maxWidths["key"] {
					maxWidths["key"] = len(fmt.Sprint(slk))
				}
				if len(fmt.Sprint(v)) > maxWidths[k] {
					maxWidths[k] = len(fmt.Sprint(v))
					if maxWidths[k] < len(k) {
						maxWidths[k] = len(k)
					}
				}
			}
		}

		if len(keyOrder) > 0 {
			fmt.Printf("%-"+fmt.Sprint(maxWidths["key"])+"s  ", "")
			for _, v := range keyOrder {
				fmt.Printf("%-"+fmt.Sprint(maxWidths[v])+"s  ", v)
			}
			fmt.Println()
		}

		for slk, slv := range tlv.(map[string]interface{}) {
			fmt.Printf("%-"+fmt.Sprint(maxWidths["key"])+"s  ", slk)
			for _, k := range keyOrder {
				preformatted := slv.(map[string]interface{})[k]
				var formatted string
				switch k {
				case "bytes_sent", "bytes_recvd":
					formatted = fmt.Sprintf("%d", uint(preformatted.(float64)))
				case "uptime", "last_seen":
					seconds := uint(preformatted.(float64)) % 60
					minutes := uint(preformatted.(float64)/60) % 60
					hours := uint(preformatted.(float64) / 60 / 60)
					formatted = fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
				default:
					formatted = fmt.Sprint(preformatted)
				}
				fmt.Printf("%-"+fmt.Sprint(maxWidths[k])+"s  ", formatted)
			}
			fmt.Println()
		}
	}
}

func handleGetAndSetTunTap(res map[string]interface{}) {
	for k, v := range res {
		fmt.Println("Interface name:", k)
		if mtu, ok := v.(map[string]interface{})["mtu"].(float64); ok {
			fmt.Println("Interface MTU:", mtu)
		}
		if tap_mode, ok := v.(map[string]interface{})["tap_mode"].(bool); ok {
			fmt.Println("TAP mode:", tap_mode)
		}
	}
}

func handleGetSelf(res map[string]interface{}, verbose bool) {
	for k, v := range res["self"].(map[string]interface{}) {
		if buildname, ok := v.(map[string]interface{})["build_name"].(string); ok && buildname != "unknown" {
			fmt.Println("Build name:", buildname)
		}
		if buildversion, ok := v.(map[string]interface{})["build_version"].(string); ok && buildversion != "unknown" {
			fmt.Println("Build version:", buildversion)
		}
		fmt.Println("IPv6 address:", k)
		if subnet, ok := v.(map[string]interface{})["subnet"].(string); ok {
			fmt.Println("IPv6 subnet:", subnet)
		}
		if boxSigKey, ok := v.(map[string]interface{})["key"].(string); ok {
			fmt.Println("Public key:", boxSigKey)
		}
		if coords, ok := v.(map[string]interface{})["coords"].(string); ok {
			fmt.Println("Coords:", coords)
		}
		if verbose {
			if nodeID, ok := v.(map[string]interface{})["node_id"].(string); ok {
				fmt.Println("Node ID:", nodeID)
			}
			if boxPubKey, ok := v.(map[string]interface{})["box_pub_key"].(string); ok {
				fmt.Println("Public encryption key:", boxPubKey)
			}
			if boxSigKey, ok := v.(map[string]interface{})["box_sig_key"].(string); ok {
				fmt.Println("Public signing key:", boxSigKey)
			}
		}
	}
}

func handleGetSwitchQueues(res map[string]interface{}) {
	maximumqueuesize := float64(4194304)
	portqueues := make(map[float64]float64)
	portqueuesize := make(map[float64]float64)
	portqueuepackets := make(map[float64]float64)
	v := res["switchqueues"].(map[string]interface{})
	if queuecount, ok := v["queues_count"].(float64); ok {
		fmt.Printf("Active queue count: %d queues\n", uint(queuecount))
	}
	if queuesize, ok := v["queues_size"].(float64); ok {
		fmt.Printf("Active queue size: %d bytes\n", uint(queuesize))
	}
	if highestqueuecount, ok := v["highest_queues_count"].(float64); ok {
		fmt.Printf("Highest queue count: %d queues\n", uint(highestqueuecount))
	}
	if highestqueuesize, ok := v["highest_queues_size"].(float64); ok {
		fmt.Printf("Highest queue size: %d bytes\n", uint(highestqueuesize))
	}
	if m, ok := v["maximum_queues_size"].(float64); ok {
		maximumqueuesize = m
		fmt.Printf("Maximum queue size: %d bytes\n", uint(maximumqueuesize))
	}
	if queues, ok := v["queues"].([]interface{}); ok {
		if len(queues) != 0 {
			fmt.Println("Active queues:")
			for _, v := range queues {
				queueport := v.(map[string]interface{})["queue_port"].(float64)
				queuesize := v.(map[string]interface{})["queue_size"].(float64)
				queuepackets := v.(map[string]interface{})["queue_packets"].(float64)
				queueid := v.(map[string]interface{})["queue_id"].(string)
				portqueues[queueport]++
				portqueuesize[queueport] += queuesize
				portqueuepackets[queueport] += queuepackets
				queuesizepercent := (100 / maximumqueuesize) * queuesize
				fmt.Printf("- Switch port %d, Stream ID: %v, size: %d bytes (%d%% full), %d packets\n",
					uint(queueport), []byte(queueid), uint(queuesize),
					uint(queuesizepercent), uint(queuepackets))
			}
		}
	}
	if len(portqueuesize) > 0 && len(portqueuepackets) > 0 {
		fmt.Println("Aggregated statistics by switchport:")
		for k, v := range portqueuesize {
			queuesizepercent := (100 / (portqueues[k] * maximumqueuesize)) * v
			fmt.Printf("- Switch port %d, size: %d bytes (%d%% full), %d packets\n",
				uint(k), uint(v), uint(queuesizepercent), uint(portqueuepackets[k]))
		}
	}
}

func handleAddsAndRemoves(res map[string]interface{}) {
	if _, ok := res["added"]; ok {
		for _, v := range res["added"].([]interface{}) {
			fmt.Println("Added:", fmt.Sprint(v))
		}
	}
	if _, ok := res["not_added"]; ok {
		for _, v := range res["not_added"].([]interface{}) {
			fmt.Println("Not added:", fmt.Sprint(v))
		}
	}
	if _, ok := res["removed"]; ok {
		for _, v := range res["removed"].([]interface{}) {
			fmt.Println("Removed:", fmt.Sprint(v))
		}
	}
	if _, ok := res["not_removed"]; ok {
		for _, v := range res["not_removed"].([]interface{}) {
			fmt.Println("Not removed:", fmt.Sprint(v))
		}
	}
}

func handleGetAllowedEncryptionPublicKeys(res map[string]interface{}) {
	if _, ok := res["allowed_box_pubs"]; !ok {
		fmt.Println("All connections are allowed")
	} else if res["allowed_box_pubs"] == nil {
		fmt.Println("All connections are allowed")
	} else {
		fmt.Println("Connections are allowed only from the following public box keys:")
		for _, v := range res["allowed_box_pubs"].([]interface{}) {
			fmt.Println("-", v)
		}
	}
}

func handleGetMulticastInterfaces(res map[string]interface{}) {
	if _, ok := res["multicast_interfaces"]; !ok {
		fmt.Println("No multicast interfaces found")
	} else if res["multicast_interfaces"] == nil {
		fmt.Println("No multicast interfaces found")
	} else {
		fmt.Println("Multicast peer discovery is active on:")
		for _, v := range res["multicast_interfaces"].([]interface{}) {
			fmt.Println("-", v)
		}
	}
}

func handleGetSourceSubnets(res map[string]interface{}) {
	if _, ok := res["source_subnets"]; !ok {
		fmt.Println("No source subnets found")
	} else if res["source_subnets"] == nil {
		fmt.Println("No source subnets found")
	} else {
		fmt.Println("Source subnets:")
		for _, v := range res["source_subnets"].([]interface{}) {
			fmt.Println("-", v)
		}
	}
}

func handleGetRoutes(res map[string]interface{}) {
	if routes, ok := res["routes"].(map[string]interface{}); !ok {
		fmt.Println("No routes found")
	} else {
		if res["routes"] == nil || len(routes) == 0 {
			fmt.Println("No routes found")
		} else {
			fmt.Println("Routes:")
			for k, v := range routes {
				if pv, ok := v.(string); ok {
					fmt.Println("-", k, " via ", pv)
				}
			}
		}
	}
}

func handleGetTunnelRouting(res map[string]interface{}) {
	if enabled, ok := res["enabled"].(bool); !ok {
		fmt.Println("Tunnel routing is disabled")
	} else if !enabled {
		fmt.Println("Tunnel routing is disabled")
	} else {
		fmt.Println("Tunnel routing is enabled")
	}
}
