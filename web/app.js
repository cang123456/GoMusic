const elements = {
  audio: document.querySelector("#audio"),
  coverImage: document.querySelector("#coverImage"),
  currentFilename: document.querySelector("#currentFilename"),
  currentTime: document.querySelector("#currentTime"),
  currentTitle: document.querySelector("#currentTitle"),
  defaultButton: document.querySelector("#defaultButton"),
  duration: document.querySelector("#duration"),
  emptyMessage: document.querySelector("#emptyMessage"),
  emptyState: document.querySelector("#emptyState"),
  fileInput: document.querySelector("#fileInput"),
  muteButton: document.querySelector("#muteButton"),
  nextButton: document.querySelector("#nextButton"),
  playButton: document.querySelector("#playButton"),
  playbackStatus: document.querySelector("#playbackStatus"),
  playingIndicator: document.querySelector("#playingIndicator"),
  previousButton: document.querySelector("#previousButton"),
  progressInput: document.querySelector("#progressInput"),
  searchInput: document.querySelector("#searchInput"),
  toast: document.querySelector("#toast"),
  trackCount: document.querySelector("#trackCount"),
  trackList: document.querySelector("#trackList"),
  uploadButton: document.querySelector("#uploadButton"),
  volumeInput: document.querySelector("#volumeInput"),
};

const state = {
  tracks: [],
  currentIndex: -1,
  toastTimer: 0,
  previousVolume: 0.8,
};

function icon(name) {
  const node = document.createElement("i");
  node.dataset.lucide = name;
  node.setAttribute("aria-hidden", "true");
  return node;
}

function refreshIcons() {
  if (window.lucide) {
    window.lucide.createIcons();
  }
}

function currentTrack() {
  return state.tracks[state.currentIndex] || null;
}

async function request(url, options) {
  const response = await fetch(url, options);
  if (!response.ok) {
    let message = "请求失败";
    try {
      const body = await response.json();
      message = body.error || message;
    } catch {
      message = `请求失败（${response.status}）`;
    }
    throw new Error(message);
  }
  return response.json();
}

async function loadTracks(selectedID = null, autoplay = true) {
  try {
    const data = await request("/api/tracks");
    state.tracks = Array.isArray(data.tracks) ? data.tracks : [];
    elements.trackCount.textContent = `${state.tracks.length} 首`;
    renderTrackList();
    setControlAvailability();

    if (state.tracks.length === 0) {
      resetPlayer();
      return;
    }

    let index = selectedID === null
      ? state.tracks.findIndex((track) => track.is_default)
      : state.tracks.findIndex((track) => track.id === selectedID);
    if (index < 0) {
      index = 0;
    }
    await loadTrack(index, autoplay);
  } catch (error) {
    showToast(error.message, true);
    elements.playbackStatus.textContent = "连接失败";
  }
}

function renderTrackList() {
  const query = elements.searchInput.value.trim().toLocaleLowerCase();
  elements.trackList.replaceChildren();

  let visibleCount = 0;
  state.tracks.forEach((track, index) => {
    const searchable = `${track.title} ${track.filename}`.toLocaleLowerCase();
    if (query && !searchable.includes(query)) {
      return;
    }
    visibleCount += 1;

    const button = document.createElement("button");
    button.type = "button";
    button.className = "track-item";
    button.dataset.index = String(index);
    button.setAttribute("aria-label", `播放 ${track.title}`);
    if (index === state.currentIndex) {
      button.classList.add("is-current");
      button.setAttribute("aria-current", "true");
    }

    const order = document.createElement("span");
    order.className = "track-index";
    order.textContent = String(index + 1).padStart(2, "0");

    const copy = document.createElement("span");
    copy.className = "track-copy";
    const title = document.createElement("span");
    title.className = "track-name";
    title.textContent = track.title;
    const filename = document.createElement("span");
    filename.className = "track-file";
    filename.textContent = track.filename;
    copy.append(title, filename);

    const marker = document.createElement("span");
    marker.className = "track-default";
    if (track.is_default) {
      marker.title = "默认歌曲";
      marker.append(icon("star"));
    }

    button.append(order, copy, marker);
    button.addEventListener("click", () => loadTrack(index, true));
    elements.trackList.append(button);
  });

  elements.emptyState.hidden = visibleCount !== 0;
  elements.emptyMessage.textContent = query ? "没有找到歌曲" : "歌曲库为空";
  refreshIcons();
}

async function loadTrack(index, autoplay) {
  if (index < 0 || index >= state.tracks.length) {
    return;
  }

  const changed = index !== state.currentIndex;
  state.currentIndex = index;
  const track = currentTrack();
  elements.currentTitle.textContent = track.title;
  elements.currentFilename.textContent = track.filename;
  elements.playbackStatus.textContent = "正在载入";
  elements.progressInput.value = "0";
  elements.currentTime.textContent = "0:00";
  elements.duration.textContent = "0:00";
  updateDefaultButton();
  renderTrackList();

  if (changed || elements.audio.src === "") {
    elements.audio.src = track.audio_url;
    elements.audio.load();
  }

  if (autoplay) {
    await playCurrent();
  } else {
    updatePlaybackUI(false);
  }
}

async function playCurrent() {
  if (!currentTrack()) {
    return;
  }
  try {
    await elements.audio.play();
  } catch (error) {
    updatePlaybackUI(false);
    if (error.name === "NotAllowedError") {
      elements.playbackStatus.textContent = "浏览器已暂停自动播放";
    } else {
      elements.playbackStatus.textContent = "无法播放";
      showToast("歌曲播放失败", true);
    }
  }
}

function togglePlayback() {
  if (!currentTrack()) {
    elements.fileInput.click();
    return;
  }
  if (elements.audio.paused) {
    playCurrent();
  } else {
    elements.audio.pause();
  }
}

function changeTrack(direction) {
  if (state.tracks.length === 0) {
    return;
  }
  const nextIndex = (state.currentIndex + direction + state.tracks.length) % state.tracks.length;
  loadTrack(nextIndex, true);
}

function updatePlaybackUI(isPlaying) {
  elements.playButton.replaceChildren(icon(isPlaying ? "pause" : "play"));
  elements.playButton.title = isPlaying ? "暂停" : "播放";
  elements.playButton.setAttribute("aria-label", isPlaying ? "暂停" : "播放");
  elements.playingIndicator.classList.toggle("is-active", isPlaying);
  if (currentTrack()) {
    elements.playbackStatus.textContent = isPlaying ? "正在播放" : "已暂停";
  }
  refreshIcons();
}

function updateDefaultButton() {
  const track = currentTrack();
  const isDefault = Boolean(track && track.is_default);
  elements.defaultButton.disabled = !track || isDefault;
  elements.defaultButton.classList.toggle("is-default", isDefault);
  elements.defaultButton.replaceChildren(icon("star"));
  const label = document.createElement("span");
  label.textContent = isDefault ? "默认歌曲" : "设为默认";
  elements.defaultButton.append(label);
  refreshIcons();
}

function setControlAvailability() {
  const disabled = state.tracks.length === 0;
  elements.previousButton.disabled = disabled;
  elements.nextButton.disabled = disabled;
  elements.progressInput.disabled = disabled;
}

function resetPlayer() {
  state.currentIndex = -1;
  elements.audio.removeAttribute("src");
  elements.audio.load();
  elements.currentTitle.textContent = "MusicGo";
  elements.currentFilename.textContent = "等待歌曲";
  elements.playbackStatus.textContent = "暂无歌曲";
  elements.progressInput.value = "0";
  elements.currentTime.textContent = "0:00";
  elements.duration.textContent = "0:00";
  updateDefaultButton();
  updatePlaybackUI(false);
  renderTrackList();
}

function formatTime(value) {
  if (!Number.isFinite(value) || value < 0) {
    return "0:00";
  }
  const minutes = Math.floor(value / 60);
  const seconds = Math.floor(value % 60);
  return `${minutes}:${String(seconds).padStart(2, "0")}`;
}

function updateTimeline() {
  const duration = elements.audio.duration;
  const current = elements.audio.currentTime;
  elements.currentTime.textContent = formatTime(current);
  elements.duration.textContent = formatTime(duration);
  elements.progressInput.value = Number.isFinite(duration) && duration > 0
    ? String(Math.round((current / duration) * 1000))
    : "0";
}

function updateVolumeIcon() {
  let iconName = "volume-2";
  if (elements.audio.muted || elements.audio.volume === 0) {
    iconName = "volume-x";
  } else if (elements.audio.volume < 0.5) {
    iconName = "volume-1";
  }
  elements.muteButton.replaceChildren(icon(iconName));
  elements.muteButton.title = elements.audio.muted ? "取消静音" : "静音";
  elements.muteButton.setAttribute("aria-label", elements.audio.muted ? "取消静音" : "静音");
  refreshIcons();
}

async function setDefault() {
  const track = currentTrack();
  if (!track || track.is_default) {
    return;
  }
  try {
    await request("/api/default", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ track_id: track.id }),
    });
    state.tracks.forEach((item) => {
      item.is_default = item.id === track.id;
    });
    updateDefaultButton();
    renderTrackList();
    showToast(`已将“${track.title}”设为默认歌曲`);
  } catch (error) {
    showToast(error.message, true);
  }
}

async function uploadTrack(file) {
  const form = new FormData();
  form.append("file", file);
  elements.uploadButton.disabled = true;
  try {
    const track = await request("/api/tracks", {
      method: "POST",
      body: form,
    });
    showToast(`已添加“${track.title}”`);
    await loadTracks(track.id, true);
  } catch (error) {
    showToast(error.message, true);
  } finally {
    elements.uploadButton.disabled = false;
    elements.fileInput.value = "";
  }
}

function showToast(message, isError = false) {
  window.clearTimeout(state.toastTimer);
  elements.toast.textContent = message;
  elements.toast.classList.toggle("is-error", isError);
  elements.toast.hidden = false;
  state.toastTimer = window.setTimeout(() => {
    elements.toast.hidden = true;
  }, 3200);
}

elements.playButton.addEventListener("click", togglePlayback);
elements.nextButton.addEventListener("click", () => changeTrack(1));
elements.previousButton.addEventListener("click", () => {
  if (elements.audio.currentTime > 3) {
    elements.audio.currentTime = 0;
    return;
  }
  changeTrack(-1);
});
elements.defaultButton.addEventListener("click", setDefault);
elements.uploadButton.addEventListener("click", () => elements.fileInput.click());
elements.fileInput.addEventListener("change", () => {
  const [file] = elements.fileInput.files;
  if (file) {
    uploadTrack(file);
  }
});
elements.searchInput.addEventListener("input", renderTrackList);
elements.progressInput.addEventListener("input", () => {
  if (Number.isFinite(elements.audio.duration)) {
    elements.audio.currentTime = (Number(elements.progressInput.value) / 1000) * elements.audio.duration;
  }
});
elements.volumeInput.addEventListener("input", () => {
  const volume = Number(elements.volumeInput.value);
  elements.audio.volume = volume;
  elements.audio.muted = false;
  if (volume > 0) {
    state.previousVolume = volume;
  }
  try {
    localStorage.setItem("musicgo-volume", String(volume));
  } catch {
    // The player still works when browser storage is unavailable.
  }
  updateVolumeIcon();
});
elements.muteButton.addEventListener("click", () => {
  if (elements.audio.muted || elements.audio.volume === 0) {
    elements.audio.muted = false;
    elements.audio.volume = state.previousVolume || 0.8;
    elements.volumeInput.value = String(elements.audio.volume);
  } else {
    state.previousVolume = elements.audio.volume;
    elements.audio.muted = true;
  }
  updateVolumeIcon();
});

elements.audio.addEventListener("play", () => updatePlaybackUI(true));
elements.audio.addEventListener("playing", () => updatePlaybackUI(true));
elements.audio.addEventListener("pause", () => updatePlaybackUI(false));
elements.audio.addEventListener("timeupdate", updateTimeline);
elements.audio.addEventListener("durationchange", updateTimeline);
elements.audio.addEventListener("ended", () => changeTrack(1));
elements.audio.addEventListener("waiting", () => {
  if (!elements.audio.paused) {
    elements.playbackStatus.textContent = "正在缓冲";
  }
});
elements.audio.addEventListener("error", () => {
  elements.playbackStatus.textContent = "无法播放";
  updatePlaybackUI(false);
});

try {
  const savedVolume = Number(localStorage.getItem("musicgo-volume"));
  if (Number.isFinite(savedVolume) && savedVolume >= 0 && savedVolume <= 1) {
    elements.audio.volume = savedVolume;
    elements.volumeInput.value = String(savedVolume);
    state.previousVolume = savedVolume || 0.8;
  } else {
    elements.audio.volume = 0.8;
  }
} catch {
  elements.audio.volume = 0.8;
}

updateVolumeIcon();
setControlAvailability();
loadTracks();
