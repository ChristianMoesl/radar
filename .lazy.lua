return {
  {
    dir = vim.fn.getcwd(),
    name = "radar.nvim",
    dev = true,
    build = "go build -o radar ./cmd/radar",
    config = function()
      require("radar").setup({
        radar_cmd = vim.fn.getcwd() .. "/radar",
        prefer_local_radar_binary = false,
      })
    end,
  },
}
