
# conda activate spl123

spl3 --hub http://192.168.1.74:9000 \
  run ~/projects/digital-duck/SPL.py/cookbook/89_arxiv_digest_eaai27/arxiv_digest.spl \
  --adapter momagrid --model gemma3 \
  -p urls='["https://arxiv.org/abs/2510.01230"]' \
  -p out_dir=~/projects/digital-duck/arxiv-digest-eaai27/output
