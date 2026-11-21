-- hire Taskfile.lua — runnable v2 sample (see docs/lua.md).
-- hire <name> runs a task; hire ls lists them.

echo = function()
  x.echo("hello from echo task")
end

quick = {
  "echo line1",
  "echo line2",
  "echo line3",
}

longrun = function()
  x.sh("echo will-sleep")
  if x.os() == "windows" then
    x.sh("Start-Sleep -Seconds 30")
  else
    x.sh("sleep 30")
  end
end

tagged = task {
  desc = "echo the tag argument",
  args = { tag = req "a version tag" },
  run = function(args)
    x.echo("tag=" .. args.tag)
  end,
}
