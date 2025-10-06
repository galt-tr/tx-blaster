#!/bin/bash

echo "Testing simple UI mode (for Warp terminal compatibility)..."
echo "This will suppress all logging output to prevent screen corruption."
echo ""
echo "Usage: ./tx-blaster blast-from-tx --key YOUR_KEY --tx YOUR_TX --vout 0 --simple-ui"
echo ""
echo "Controls:"
echo "  P - Pause/Resume"
echo "  R - Change rate"
echo "  Q - Quit"
echo "  H - Help"
echo ""
echo "The simple UI uses fixed positioning to avoid terminal compatibility issues."