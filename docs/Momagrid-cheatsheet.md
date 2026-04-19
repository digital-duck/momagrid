
```bash

git clone git@github.com:digital-duck/SPL.py.gitgit clone git@github.com:digital-duck/SPL.ts.gitgit clone git@github.com:digital-duck/SPL.go.gitgit clone git@github.com:digital-duck/momagrid.git
Build pip install in SPL.py
go build in momagrid and SPL.go
npm install npm run build
# symlink ln -s ~/projects/digital-duck/momagrid/mg ~/.local/bin/mgln -s ~/projects/digital-duck/SPL.go/spl-go ~/.local/bin/spl-goln -s ~/projects/digital-duck/SPL.ts/spl-ts ~/.local/bin/spl-ts
# launch Momagrid Hub in one of GPU nodemy hub up
# Other machines can join with:
mg join http://192.168.0.184:9000

(base) gongai@ducklover1:~/projects/digital-duck/SPL.py$ mg status
# Hub: hub-b5f1e79e  Status: ok  Agents: 1
(base) gongai@ducklover1:~/projects/digital-duck/SPL.py$ mg agents
# NAME             AGENT_ID                               TIER       STATUS        TPS
# ducklover1       agent-6fabe241                         GOLD       ONLINE        0.0
(base) gongai@ducklover1:~/projects/digital-duck/SPL.py$ mg tasks
# No tasks.
```