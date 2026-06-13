local M = {}

local config = {
	radar_cmd = "radar",
	auto_start = true,
	refresh_ms = 30000,
	width = 140,
	height = 0.85,
	padding_x = 1,
	notify_new_items = true,
	prefer_local_radar_binary = true,
	auto_reload_binary = true,
	icons = {
		immediate = "🚨",
		attention = "👀",
		in_progress = "⏳",
		done = "✅",
		low_priority = "🔇",
	},
}

local state = {
	summary = { immediate = 0, attention = 0, in_progress = 0, done = 0, low_priority = 0 },
	items = {},
	sources = {
		{ name = "github", status = "initializing" },
		{ name = "jira", status = "initializing" },
		{ name = "git", status = "initializing" },
	},
	timer = nil,
	buf = nil,
	win = nil,
	line_items = {},
	line_highlights = {},
	seen_items = {},
	seen_items_initialized = false,
	radar_binary_path = nil,
	radar_binary_mtime = nil,
}

local ns = vim.api.nvim_create_namespace("radar")

local function decode_json(text)
	local ok, decoded = pcall(vim.json.decode, text)
	if ok then
		return decoded
	end
end

local function run(args, cb)
	vim.system(args, { text = true }, function(result)
		vim.schedule(function()
			cb(result)
		end)
	end)
end

local function plugin_root()
	local source = debug.getinfo(1, "S").source:gsub("^@", "")
	return vim.fn.fnamemodify(source, ":p:h:h:h")
end

local function executable(path)
	return path and vim.fn.executable(path) == 1
end

local function resolve_radar_cmd()
	if config.prefer_local_radar_binary and config.radar_cmd == "radar" then
		local local_binary = plugin_root() .. "/radar"
		if executable(local_binary) then
			return local_binary
		end
	end
	return config.radar_cmd
end

local function binary_mtime(path)
	local stat = (vim.uv or vim.loop).fs_stat(path)
	if stat then
		return stat.mtime.sec
	end
end

local function track_radar_binary()
	local cmd = resolve_radar_cmd()
	state.radar_binary_path = executable(cmd) and cmd or vim.fn.exepath(cmd)
	state.radar_binary_mtime = binary_mtime(state.radar_binary_path)
end

local function start_daemon()
	if not config.auto_start then
		return
	end
	vim.fn.jobstart({ resolve_radar_cmd(), "daemon" }, { detach = true })
end

local function restart_daemon(cb)
	vim.fn.jobstart({ resolve_radar_cmd(), "restart" }, {
		detach = true,
		on_exit = vim.schedule_wrap(function()
			track_radar_binary()
			if cb then
				cb()
			else
				M.load()
			end
		end),
	})
end

local function stop_daemon()
	vim.fn.jobstart({ resolve_radar_cmd(), "stop" }, { detach = true })
end

local function stop_timer()
	if state.timer then
		state.timer:stop()
		state.timer:close()
		state.timer = nil
	end
end

local function is_open()
	return state.win and vim.api.nvim_win_is_valid(state.win)
end

local function close_window()
	if is_open() then
		vim.api.nvim_win_close(state.win, true)
	end
	state.win = nil
end

local function open_url(url)
	if not url or url == "" then
		vim.notify("Radar line has no URL", vim.log.levels.WARN)
		return
	end

	if vim.ui and vim.ui.open then
		vim.ui.open(url)
	else
		vim.fn.jobstart({ "xdg-open", url }, { detach = true })
	end
end

local function tmux_session_target(item)
	if not item then
		return nil
	end
	if item.source == "tmux" and item.kind == "session" then
		return item.metadata and (item.metadata.switch_target or item.metadata.session) or item.title
	end
	for _, sourceRef in ipairs(item.source_refs or {}) do
		if sourceRef.source == "tmux" and sourceRef.kind == "session" then
			return sourceRef.metadata and (sourceRef.metadata.switch_target or sourceRef.metadata.session) or sourceRef.title
		end
	end
end

local function switch_tmux_session(target)
	if not target or target == "" then
		vim.notify("Radar tmux session has no target", vim.log.levels.WARN)
		return
	end
	run({ "tmux", "switch-client", "-t", target }, function(result)
		if result.code ~= 0 then
			vim.notify("Could not switch tmux session: " .. (result.stderr or ""), vim.log.levels.ERROR)
		end
	end)
end

local function open_filters()
	run({ resolve_radar_cmd(), "filters-path" }, function(result)
		if result.code ~= 0 then
			vim.notify("Could not open Radar filters: " .. (result.stderr or ""), vim.log.levels.ERROR)
			return
		end
		local path = vim.trim(result.stdout or "")
		if path == "" then
			vim.notify("Radar filters path is empty", vim.log.levels.ERROR)
			return
		end
		vim.cmd.edit(vim.fn.fnameescape(path))
	end)
end

local function item_icon(attention)
	return config.icons[attention] or "•"
end

local function item_label(attention)
	return ({
		immediate = "Needs immediate attention",
		attention = "Needs attention",
		in_progress = "In progress",
		done = "Done today",
		low_priority = "Low priority",
	})[attention] or attention or "Unknown"
end

local function item_status(attention)
	return ({
		immediate = "urgent",
		attention = "attention",
		in_progress = "progress",
		done = "done",
		low_priority = "low",
	})[attention] or attention or "unknown"
end

local function item_id(item)
	return item.id or string.format("%s:%s:%s", item.kind or "", item.repo or "", item.title or "")
end

local function notification_level(item)
	if item.attention == "immediate" then
		return vim.log.levels.WARN
	end
	return vim.log.levels.INFO
end

local function notify_new_items(items)
	local next_seen = {}
	local new_items = {}

	for _, item in ipairs(items) do
		local id = item_id(item)
		next_seen[id] = true
		if state.seen_items_initialized and not state.seen_items[id] then
			table.insert(new_items, item)
		end
	end

	state.seen_items = next_seen
	state.seen_items_initialized = true

	if not config.notify_new_items then
		return
	end

	for _, item in ipairs(new_items) do
		vim.notify(
			string.format("%s %s\n%s", item_icon(item.attention), item.title or "Untitled", item.reason or item_label(item.attention)),
			notification_level(item),
			{ title = "New Radar task" }
		)
	end
end

local function append_field(fields, label, value)
	if value and value ~= "" then
		table.insert(fields, string.format("%s=%s", label, value))
	end
end

local function sourceRef_identifier(sourceRef)
	return sourceRef.id or sourceRef.title or sourceRef.repo or sourceRef.path or sourceRef.branch or "unknown"
end

local function add_highlight(line_highlights, line, start_col, end_col, group)
	table.insert(line_highlights, {
		line = line,
		start_col = start_col,
		end_col = end_col,
		group = group,
	})
end

local function source_status_hl(status)
	return ({
		ok = "RadarSourceStatusOK",
		paused = "RadarSourceStatusWarn",
		error = "RadarSourceStatusError",
		disabled = "RadarSourceStatusDisabled",
		initializing = "RadarSourceStatusInitializing",
	})[status] or "RadarSourceStatusDisabled"
end

local function add_source(lines, line_highlights, source)
	local name = source.name or "unknown"
	local status = source.status or "unknown"
	local source_ref_count = source.source_ref_count or 0
	local detail = source.detail or ""
	local prefix = string.format("  %-8s ", name)
	local status_text = string.format("%-8s", status)
	local count_text = string.format("%4d refs", source_ref_count)
	local line = prefix .. status_text .. "  " .. count_text
	if detail ~= "" then
		line = line .. "  " .. detail
	end

	table.insert(lines, line)
	add_highlight(line_highlights, #lines, 2, 2 + #name, "RadarSourceName")
	add_highlight(line_highlights, #lines, #prefix, #prefix + #status, source_status_hl(status))
end

local function add_item(lines, line_items, line_highlights, item)
	local fields = {}
	append_field(fields, "reason", item.reason)

	local prefix = "  "
	local title = item.title or "Untitled"
	local line = prefix .. title
	if #fields > 0 then
		line = line .. "  " .. table.concat(fields, "  ")
	end

	table.insert(lines, line)
	line_items[#lines] = item
	local title_hl = item.attention == "low_priority" and "RadarLowPriorityItemTitle" or "RadarItemTitle"
	local sourceRef_hl = item.attention == "low_priority" and "RadarLowPrioritySourceRefIdentifier" or "RadarSourceRefIdentifier"
	add_highlight(line_highlights, #lines, #prefix, #prefix + #title, title_hl)

	for _, sourceRef in ipairs(item.source_refs or {}) do
		local sourceRef_prefix = "  ↳ "
		local identifier = sourceRef_identifier(sourceRef)
		local sourceRef_line = sourceRef_prefix .. identifier

		table.insert(lines, sourceRef_line)
		line_items[#lines] = sourceRef
		add_highlight(line_highlights, #lines, #sourceRef_prefix, #sourceRef_prefix + #identifier, sourceRef_hl)
	end
end

local function render_lines()
	local s = state.summary
	local lines = {
		string.format("Radar  %s %d urgent  %s %d attention  %s %d progress  %s %d done  %s %d low    <CR>: open  r: refresh  f: filters  q: close", config.icons.immediate, s.immediate or 0, config.icons.attention, s.attention or 0, config.icons.in_progress, s.in_progress or 0, config.icons.done, s.done or 0, config.icons.low_priority, s.low_priority or 0),
		"",
	}
	local line_items = {}
	local line_highlights = {}
	local groups = {
		{ key = "immediate", title = "Need immediate attention", icon = config.icons.immediate },
		{ key = "attention", title = "Need attention", icon = config.icons.attention },
		{ key = "in_progress", title = "In progress", icon = config.icons.in_progress },
		{ key = "done", title = "Done today", icon = config.icons.done },
		{ key = "low_priority", title = "Low priority", icon = config.icons.low_priority },
	}

	local rendered_groups = 0
	for _, group in ipairs(groups) do
		local added = false
		for _, item in ipairs(state.items) do
			if item.attention == group.key then
				if not added then
					if rendered_groups > 0 then
						table.insert(lines, "")
					end
					local icon = group.icon or ""
					local title = icon ~= "" and string.format("%s %s", icon, group.title) or group.title
					table.insert(lines, title)
					table.insert(lines, string.rep("─", vim.fn.strdisplaywidth(title)))
					added = true
					rendered_groups = rendered_groups + 1
				end
				add_item(lines, line_items, line_highlights, item)
			end
		end
	end

	if #state.items == 0 then
		table.insert(lines, "No tasks need your attention.")
	end

	if #state.sources > 0 then
		table.insert(lines, "")
		local title = "Sources"
		table.insert(lines, title)
		table.insert(lines, string.rep("─", #title))
		for _, source in ipairs(state.sources) do
			add_source(lines, line_highlights, source)
		end
	end

	return lines, line_items, line_highlights
end

local function sanitize_line(line)
	line = tostring(line or "")
	line = line:gsub("\r\n", " "):gsub("[\r\n]", " ")
	return line
end

local function render_window()
	if not state.buf or not vim.api.nvim_buf_is_valid(state.buf) then
		state.buf = vim.api.nvim_create_buf(false, true)
	end

	local lines, line_items, line_highlights = render_lines()
	local padding = string.rep(" ", config.padding_x)
	for i, line in ipairs(lines) do
		lines[i] = padding .. sanitize_line(line) .. padding
	end
	state.line_items = line_items
	state.line_highlights = line_highlights

	vim.bo[state.buf].modifiable = true
	vim.api.nvim_buf_set_lines(state.buf, 0, -1, false, lines)
	vim.api.nvim_buf_clear_namespace(state.buf, ns, 0, -1)
	for _, highlight in ipairs(line_highlights) do
		vim.api.nvim_buf_add_highlight(state.buf, ns, highlight.group, highlight.line - 1, highlight.start_col + config.padding_x, highlight.end_col + config.padding_x)
	end
	vim.bo[state.buf].modifiable = false
	vim.bo[state.buf].buftype = "nofile"
	vim.bo[state.buf].bufhidden = "wipe"
	vim.bo[state.buf].swapfile = false
	vim.bo[state.buf].filetype = "radar"
end

local function ensure_window()
	render_window()
	if is_open() then
		vim.api.nvim_set_current_win(state.win)
		return
	end

	local width = math.min(config.width, vim.o.columns - 4)
	local max_height = vim.o.lines - 4
	local height = config.height <= 1 and math.floor(max_height * config.height) or math.min(config.height, max_height)
	local row = math.floor((vim.o.lines - height) / 2)
	local col = math.floor((vim.o.columns - width) / 2)

	state.win = vim.api.nvim_open_win(state.buf, true, {
		relative = "editor",
		style = "minimal",
		border = "rounded",
		title = " Radar ",
		title_pos = "center",
		width = width,
		height = height,
		row = row,
		col = col,
	})

	vim.wo[state.win].wrap = false
	vim.wo[state.win].cursorline = true

	vim.keymap.set("n", "q", close_window, { buffer = state.buf, silent = true })
	vim.keymap.set("n", "<Esc>", close_window, { buffer = state.buf, silent = true })
	vim.keymap.set("n", "r", function()
		M.refresh(function()
			render_window()
		end)
	end, { buffer = state.buf, silent = true })
	vim.keymap.set("n", "f", open_filters, { buffer = state.buf, silent = true })
	vim.keymap.set("n", "<CR>", function()
		local line = vim.api.nvim_win_get_cursor(0)[1]
		local item = state.line_items[line]
		if item then
			local tmux_target = tmux_session_target(item)
			if tmux_target then
				switch_tmux_session(tmux_target)
				close_window()
				return
			end
			run({ resolve_radar_cmd(), "ack", tostring(item.id) }, function(result)
				if result.code == 0 then
					local response = decode_json(result.stdout)
					if response then
						state.summary = response.summary or state.summary
						state.items = response.tasks or state.items
						state.sources = response.sources or state.sources
						render_window()
					end
				end
			end)
			open_url(item.url)
		end
	end, { buffer = state.buf, silent = true })
end

local function refresh_statusline()
	vim.cmd("redrawstatus")
	local ok, lualine = pcall(require, "lualine")
	if ok then
		lualine.refresh({ place = { "statusline" } })
	end
end

local function setup_highlights()
	vim.api.nvim_set_hl(0, "RadarItemTitle", { link = "Title", default = true })
	vim.api.nvim_set_hl(0, "RadarLowPriorityItemTitle", { link = "Comment", default = true })
	vim.api.nvim_set_hl(0, "RadarSourceRefIdentifier", { link = "Identifier", default = true })
	vim.api.nvim_set_hl(0, "RadarLowPrioritySourceRefIdentifier", { link = "Comment", default = true })
	vim.api.nvim_set_hl(0, "RadarSourceName", { link = "Type", default = true })
	vim.api.nvim_set_hl(0, "RadarSourceStatusOK", { link = "String", default = true })
	vim.api.nvim_set_hl(0, "RadarSourceStatusWarn", { link = "WarningMsg", default = true })
	vim.api.nvim_set_hl(0, "RadarSourceStatusError", { link = "ErrorMsg", default = true })
	vim.api.nvim_set_hl(0, "RadarSourceStatusDisabled", { link = "Comment", default = true })
	vim.api.nvim_set_hl(0, "RadarSourceStatusInitializing", { link = "Comment", default = true })
end


local function fetch(method, cb, retried)
	if config.auto_reload_binary and state.radar_binary_path then
		local current_mtime = binary_mtime(state.radar_binary_path)
		if current_mtime and state.radar_binary_mtime and current_mtime ~= state.radar_binary_mtime then
			restart_daemon(function()
				fetch(method, cb, true)
			end)
			return
		end
	end

	run({ resolve_radar_cmd(), method }, function(result)
		if result.code ~= 0 then
			start_daemon()
			if not retried then
				vim.defer_fn(function()
					fetch(method, cb, true)
				end, 300)
				return
			end
			if cb then
				cb(false)
			end
			return
		end

		local response = decode_json(result.stdout)
		if not response or not response.ok then
			if cb then
				cb(false)
			end
			return
		end

		state.summary = response.summary or state.summary
		local tasks = response.tasks
		if tasks then
			notify_new_items(tasks)
			state.items = tasks
		end
		if response.sources then
			state.sources = response.sources
		end
		refresh_statusline()
		if is_open() then
			render_window()
		end
		if cb then
			cb(true)
		end
	end)
end

function M.refresh(cb)
	fetch("refresh", cb)
end

function M.reset(cb)
	fetch("reset", cb)
end

function M.load(cb)
	fetch("tasks", cb)
end

function M.statusline()
	local s = state.summary
	local status = string.format("%s%d %s%d %s%d %s%d", config.icons.immediate, s.immediate or 0, config.icons.attention, s.attention or 0, config.icons.in_progress, s.in_progress or 0, config.icons.done, s.done or 0)
	if (s.low_priority or 0) > 0 then
		status = string.format("%s %s%d", status, config.icons.low_priority, s.low_priority or 0)
	end
	return status
end

function M.open()
	ensure_window()
	fetch("tasks", function()
		render_window()
	end)
end

function M.teardown()
	stop_timer()
	close_window()
end

function M.setup(opts)
	config = vim.tbl_deep_extend("force", config, opts or {})
	stop_timer()
	setup_highlights()

	vim.api.nvim_create_user_command("Radar", M.open, {})
	vim.api.nvim_create_user_command("RadarRefresh", function()
		M.refresh()
	end, {})
	vim.api.nvim_create_user_command("RadarReset", function()
		M.reset()
	end, {})
	vim.api.nvim_create_user_command("RadarFilters", open_filters, {})
	vim.api.nvim_create_user_command("RadarStart", start_daemon, {})
	vim.api.nvim_create_user_command("RadarStop", stop_daemon, {})
	vim.api.nvim_create_user_command("RadarRestart", function()
		restart_daemon()
	end, {})

	track_radar_binary()
	M.load()

	state.timer = vim.loop.new_timer()
	state.timer:start(
		config.refresh_ms,
		config.refresh_ms,
		vim.schedule_wrap(function()
			M.load()
		end)
	)
end

return M
