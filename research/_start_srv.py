import subprocess, time, os, sys

sys.stdout.reconfigure(encoding="utf-8")
exe = r"C:\Users\Administrator\notion2api\notion2api.exe"
err = open(r"C:\Users\Administrator\notion2api\stderr.log", "ab", buffering=0)
out = open(r"C:\Users\Administrator\notion2api\stdout.log", "ab", buffering=0)
p = subprocess.Popen([exe, "--config", r"C:\Users\Administrator\notion2api\config.json"],
                     cwd=r"C:\Users\Administrator\notion2api",
                     stdout=out, stderr=err)
with open(r"C:\Users\Administrator\notion2api\server.pid", "w") as f:
    f.write(str(p.pid))
print("started pid", p.pid)