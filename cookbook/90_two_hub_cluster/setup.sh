#!/usr/bin/env bash
# Recipe 03: Two-hub cluster
HUB_A_URL="http://192.168.1.10:9000"
HUB_B_URL="http://192.168.1.20:9000"
echo "Step 1: mg hub up --hub-url $HUB_A_URL  (on Machine A)"
echo "Step 2: mg hub up --hub-url $HUB_B_URL  (on Machine B)"
echo "Step 3: mg join $HUB_B_URL --host 192.168.1.20 --port 8100  (on Machine B)"
echo "Step 4: mg peer add $HUB_B_URL --hub-url $HUB_A_URL"
echo "Step 5: mg submit 'Hello from hub A' --model llama3 --hub-url $HUB_A_URL"
echo "Step 6: mg peer list --hub-url $HUB_A_URL"
