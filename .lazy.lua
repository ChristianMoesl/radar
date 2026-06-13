return {
  {
    dir = vim.fn.getcwd(),
    name = "radar.nvim",
    dev = true,
    build = "go build -o radar ./cmd/radar",
    config = function()
      local root = vim.fn.getcwd()
      local runtime = root .. "/.radar"
      vim.env.RADAR_SOCKET = runtime .. "/radar.sock"
      vim.env.RADAR_PID = runtime .. "/radar.pid"
      vim.env.RADAR_STATE = runtime .. "/tasks.json"
      vim.env.RADAR_LOG = runtime .. "/radar.log"

      local build = vim.fn.system({ "go", "build", "-o", root .. "/radar", "./cmd/radar" })
      if vim.v.shell_error ~= 0 then
        vim.notify("Could not build local Radar binary:\n" .. build, vim.log.levels.ERROR)
        return
      end

      require("radar").setup({
        radar_cmd = root .. "/radar",
        prefer_local_radar_binary = false,
      })
    end,
  },
}
