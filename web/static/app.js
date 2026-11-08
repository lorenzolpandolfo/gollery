(function () {
  "use strict";

  var root = document.documentElement;

  function $(id) {
    return document.getElementById(id);
  }

  var fileList = $("file-list");

  function safeGet(key) {
    try {
      return localStorage.getItem(key);
    } catch (e) {
      return null;
    }
  }

  function safeSet(key, value) {
    try {
      localStorage.setItem(key, value);
    } catch (e) {}
  }

  function formatBytes(n) {
    if (n < 1024) return n + " B";
    var units = ["KB", "MB", "GB", "TB"];
    var value = n / 1024;
    var i = 0;
    while (value >= 1024 && i < units.length - 1) {
      value /= 1024;
      i++;
    }
    return value.toFixed(1).replace(".", ",") + " " + units[i];
  }

  function plural(n, one, many) {
    return n === 1 ? one : many;
  }

  function currentPath() {
    return $("path-input").value;
  }

  /* ---------------- Tema ---------------- */

  $("theme-toggle").addEventListener("click", function () {
    var next = root.dataset.theme === "dark" ? "light" : "dark";
    root.dataset.theme = next;
    safeSet("gollery-theme", next);
  });

  /* ---------------- Grade / lista ---------------- */

  var viewButtons = document.querySelectorAll(".view-toggle button");

  function syncViewButtons() {
    Array.prototype.forEach.call(viewButtons, function (button) {
      button.setAttribute(
        "aria-pressed",
        String(button.dataset.view === root.dataset.view)
      );
    });
  }

  Array.prototype.forEach.call(viewButtons, function (button) {
    button.addEventListener("click", function () {
      root.dataset.view = button.dataset.view;
      safeSet("gollery-view", button.dataset.view);
      syncViewButtons();
    });
  });

  syncViewButtons();

  /* ---------------- Ordenação e pesquisa ---------------- */

  var sortForm = $("sort-form");
  var sortSelect = $("sort-select");
  var orderInput = $("order-input");
  var orderToggle = $("order-toggle");
  var searchInput = $("search-input");
  var searchTimer = null;

  function refreshList() {
    if (window.htmx) htmx.trigger(sortForm, "refresh");
  }

  function syncOrderUI() {
    orderToggle.dataset.order = orderInput.value;
    orderToggle.title =
      orderInput.value === "asc"
        ? "Ordem crescente (clique para inverter)"
        : "Ordem decrescente (clique para inverter)";
  }

  function saveSort() {
    var by = sortSelect.value;
    var order = orderInput.value;

    safeSet("gollery-sort", JSON.stringify({ by: by, order: order }));

    // O servidor usa este cookie para já renderizar a página ordenada.
    try {
      document.cookie =
        "gollery_sort=" + by + "." + order + "; path=/; max-age=31536000; SameSite=Lax";
    } catch (e) {}
  }

  sortSelect.addEventListener("change", saveSort);

  orderToggle.addEventListener("click", function () {
    orderInput.value = orderInput.value === "asc" ? "desc" : "asc";
    syncOrderUI();
    saveSort();
    refreshList();
  });

  // Pesquisa enquanto digita (com debounce). O "change" do campo é bloqueado
  // para não disparar uma segunda requisição ao sair do campo.
  searchInput.addEventListener("input", function () {
    clearTimeout(searchTimer);
    searchTimer = setTimeout(refreshList, 300);
  });

  searchInput.addEventListener("change", function (e) {
    e.stopPropagation();
  });

  sortForm.addEventListener("submit", function (e) {
    e.preventDefault();
    clearTimeout(searchTimer);
    refreshList();
  });

  syncOrderUI();

  document.addEventListener("DOMContentLoaded", function () {
    // Restaura a ordenação salva caso o servidor tenha renderizado outra.
    var saved;
    try {
      saved = JSON.parse(safeGet("gollery-sort"));
    } catch (e) {
      saved = null;
    }

    if (!saved) return;

    var validBy = ["date", "name", "type"].indexOf(saved.by) !== -1;
    var validOrder = saved.order === "asc" || saved.order === "desc";

    if (!validBy || !validOrder) return;
    if (saved.by === sortSelect.value && saved.order === orderInput.value) return;

    sortSelect.value = saved.by;
    orderInput.value = saved.order;
    syncOrderUI();
    saveSort();

    // Dá tempo ao HTMX de processar a página antes de disparar a requisição.
    setTimeout(refreshList, 0);
  });

  /* ---------------- Modais ---------------- */

  function closeOnBackdrop(dialog) {
    dialog.addEventListener("click", function (e) {
      if (e.target === dialog) dialog.close("cancel");
    });
  }

  var confirmDialog = $("confirm-dialog");
  var doneDialog = $("upload-done");
  var folderDialog = $("folder-dialog");
  var renameDialog = $("rename-dialog");

  closeOnBackdrop(confirmDialog);
  closeOnBackdrop(doneDialog);
  closeOnBackdrop(folderDialog);
  closeOnBackdrop(renameDialog);

  function confirmDeletion(name, isFolder, permanent) {
    return new Promise(function (resolve) {
      $("confirm-title").textContent = permanent
        ? "Excluir definitivamente?"
        : "Mover para a lixeira?";

      var subject = name ? "“" + name + "”" : isFolder ? "Essa pasta" : "Esse arquivo";

      $("confirm-text").textContent = permanent
        ? (isFolder
          ? subject + " e tudo o que está dentro dela serão excluídos permanentemente. Essa ação não pode ser desfeita."
          : subject + " será excluído permanentemente. Essa ação não pode ser desfeita.")
        : (isFolder
          ? subject + " e tudo o que está dentro dela serão movidos para a lixeira e apagados após 30 dias."
          : subject + " será movido para a lixeira e apagado após 30 dias.");
      $("confirm-action").textContent = permanent ? "Excluir" : "Mover para lixeira";

      confirmDialog.returnValue = "";
      confirmDialog.addEventListener(
        "close",
        function () {
          resolve(confirmDialog.returnValue === "confirm");
        },
        { once: true }
      );
      confirmDialog.showModal();
    });
  }

  // Intercepta o hx-confirm do HTMX e usa o modal em vez do confirm() nativo.
  document.body.addEventListener("htmx:confirm", function (e) {
    if (!e.detail.question) return;

    e.preventDefault();

    var elt = e.detail.elt;
    var name = elt && elt.dataset.name;
    var isFolder = !!elt && elt.dataset.kind === "folder";
    var permanent = !!elt && elt.dataset.permanent === "true";

    confirmDeletion(name, isFolder, permanent).then(function (confirmed) {
      if (confirmed) e.detail.issueRequest(true);
    });
  });

  document.body.addEventListener("htmx:responseError", function (e) {
    var xhr = e.detail && e.detail.xhr;
    if (xhr && xhr.responseText) {
      alert(xhr.responseText.trim());
    }
  });

  /* ---------------- Nova pasta ---------------- */

  var folderForm = $("folder-form");
  var folderName = $("folder-name");
  var folderError = $("folder-error");

  function showFolderError(message) {
    folderError.textContent = message;
    folderError.hidden = false;
  }

  $("new-folder").addEventListener("click", function () {
    folderName.value = "";
    folderError.hidden = true;
    folderDialog.showModal();
    folderName.focus();
  });

  $("folder-cancel").addEventListener("click", function () {
    folderDialog.close();
  });

  var renameForm = $("rename-form");
  var renameName = $("rename-name");
  var renameError = $("rename-error");

  function showRenameError(message) {
    renameError.textContent = message;
    renameError.hidden = false;
  }

  fileList.addEventListener("click", function (e) {
    var button = e.target.closest(".rename-button");
    if (!button) return;
    e.preventDefault();
    $("rename-kind").value = button.dataset.kind;
    $("rename-path").value = button.dataset.path;
    renameName.value = button.dataset.name;
    renameError.hidden = true;
    renameDialog.showModal();
    renameName.focus();
    renameName.select();
  });

  $("rename-cancel").addEventListener("click", function () {
    renameDialog.close();
  });

  renameForm.addEventListener("submit", async function (e) {
    e.preventDefault();
    var name = renameName.value.trim();
    if (!name) {
      showRenameError("Digite um nome.");
      return;
    }

    try {
      var response = await fetch("/rename", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          kind: $("rename-kind").value,
          path: $("rename-path").value,
          name: name,
        }),
      });

      if (response.ok) {
        renameDialog.close();
        refreshList();
        return;
      }

      if (response.status === 409) {
        showRenameError("Já existe um item com esse nome nessa pasta.");
      } else if (response.status === 400) {
        showRenameError("Nome inválido. Não use / ou \\ e não comece com ponto.");
      } else if (response.status === 404) {
        showRenameError("O item não foi encontrado.");
      } else {
        showRenameError("Não foi possível renomear o item.");
      }
    } catch (err) {
      showRenameError("Falha de conexão.");
    }
  });

  folderForm.addEventListener("submit", async function (e) {
    e.preventDefault();

    var name = folderName.value.trim();

    if (!name) {
      showFolderError("Digite um nome para a pasta.");
      return;
    }

    try {
      var response = await fetch("/folders", {
        method: "POST",
        body: new URLSearchParams({ path: currentPath(), name: name }),
      });

      if (response.ok) {
        folderDialog.close();
        refreshList();
        return;
      }

      if (response.status === 409) {
        showFolderError("Já existe uma pasta com esse nome aqui.");
      } else if (response.status === 400) {
        showFolderError("Nome inválido. Não use / ou \\ e não comece com ponto.");
      } else if (response.status === 507) {
        showFolderError("Sem espaço suficiente no servidor.");
      } else {
        showFolderError("Não foi possível criar a pasta.");
      }
    } catch (err) {
      showFolderError("Falha de conexão.");
    }
  });

  /* ---------------- Upload ---------------- */

  var fileInput = $("file-input");

  var panel = $("upload-panel");
  var uploadTitle = $("upload-title");
  var uploadFilename = $("upload-filename");
  var uploadFilePct = $("upload-file-pct");
  var uploadFileBar = $("upload-file-bar");
  var uploadTotalText = $("upload-total-text");
  var uploadTotalPct = $("upload-total-pct");
  var uploadTotalBar = $("upload-total-bar");

  var uploading = false;
  var cancelled = false;
  var currentXhr = null;

  function percent(done, total) {
    if (total <= 0) return 100;
    return Math.max(0, Math.min(100, Math.round((done / total) * 100)));
  }

  function setBar(bar, pctLabel, done, total) {
    var pct = percent(done, total);
    bar.style.width = pct + "%";
    bar.parentNode.setAttribute("aria-valuenow", String(pct));
    pctLabel.textContent = pct + "%";
  }

  function errorMessage(xhr) {
    if (xhr.status === 507) return "sem espaço suficiente no servidor";
    if (xhr.status === 404) return "a pasta de destino não existe mais";
    var text = (xhr.responseText || "").trim();
    return text || "erro " + xhr.status;
  }

  function sendFile(file, destination, onProgress) {
    return new Promise(function (resolve, reject) {
      var xhr = new XMLHttpRequest();
      currentXhr = xhr;

      xhr.open("POST", "/upload?path=" + encodeURIComponent(destination));

      xhr.upload.onprogress = function (e) {
        if (!e.lengthComputable || e.total === 0) return;
        // e.loaded inclui o overhead do multipart; normaliza para o tamanho do arquivo.
        onProgress((e.loaded / e.total) * file.size);
      };

      xhr.onload = function () {
        currentXhr = null;
        if (xhr.status >= 200 && xhr.status < 300) {
          onProgress(file.size);
          resolve();
        } else {
          reject(new Error(errorMessage(xhr)));
        }
      };

      xhr.onerror = function () {
        currentXhr = null;
        reject(new Error("falha de conexão"));
      };

      xhr.onabort = function () {
        currentXhr = null;
        reject({ cancelled: true });
      };

      var data = new FormData();
      data.append("Files", file, file.name);
      xhr.send(data);
    });
  }

  function showDone(result) {
    var title;
    var text;

    if (result.cancelled) {
      title = "Upload cancelado";
      text =
        result.ok + " de " + result.total + " " + plural(result.total, "arquivo enviado", "arquivos enviados") + " antes do cancelamento.";
    } else if (result.errors.length === 0) {
      title = "Upload concluído";
      text =
        result.ok + " " + plural(result.ok, "arquivo enviado", "arquivos enviados") + " com sucesso.";
    } else if (result.ok === 0) {
      title = "Falha no upload";
      text = "Nenhum arquivo foi enviado.";
    } else {
      title = "Upload concluído com erros";
      text =
        result.ok + " de " + result.total + " " + plural(result.total, "arquivo enviado", "arquivos enviados") + ".";
    }

    $("done-title").textContent = title;
    $("done-text").textContent = text;

    var list = $("done-errors");
    list.textContent = "";

    result.errors.forEach(function (item) {
      var li = document.createElement("li");
      var name = document.createElement("strong");
      name.textContent = item.name;
      li.appendChild(name);
      li.appendChild(document.createTextNode(" — " + item.reason));
      list.appendChild(li);
    });

    list.hidden = result.errors.length === 0;

    doneDialog.showModal();
  }

  async function startUpload(files) {
    uploading = true;
    cancelled = false;
    fileInput.disabled = true;

    // A pasta de destino é fixada no início, mesmo que a pesquisa mude depois.
    var destination = currentPath();

    var total = files.reduce(function (sum, f) {
      return sum + f.size;
    }, 0);

    var completedBytes = 0;
    var ok = 0;
    var errors = [];

    panel.hidden = false;
    setBar(uploadTotalBar, uploadTotalPct, 0, total);
    uploadTotalText.textContent = "0 B de " + formatBytes(total);

    function updateTotal(loaded) {
      var done = completedBytes + loaded;
      setBar(uploadTotalBar, uploadTotalPct, done, total);
      uploadTotalText.textContent = formatBytes(done) + " de " + formatBytes(total);
    }

    for (var i = 0; i < files.length && !cancelled; i++) {
      var file = files[i];

      uploadTitle.textContent = "Enviando " + (i + 1) + " de " + files.length;
      uploadFilename.textContent = file.name;
      uploadFilename.title = file.name;
      setBar(uploadFileBar, uploadFilePct, 0, file.size);

      try {
        await sendFile(file, destination, function (loaded) {
          setBar(uploadFileBar, uploadFilePct, loaded, file.size);
          updateTotal(loaded);
        });
        ok++;
      } catch (err) {
        if (err && err.cancelled) break;
        errors.push({ name: file.name, reason: err.message });
      }

      completedBytes += file.size;
      updateTotal(0);
    }

    var wasCancelled = cancelled;

    panel.hidden = true;
    fileInput.disabled = false;
    uploading = false;
    cancelled = false;

    if (ok > 0) refreshList();

    showDone({
      ok: ok,
      total: files.length,
      errors: errors,
      cancelled: wasCancelled,
    });
  }

  fileInput.addEventListener("change", function () {
    var files = Array.prototype.slice.call(fileInput.files);
    fileInput.value = "";

    if (files.length > 0 && !uploading) startUpload(files);
  });

  $("upload-cancel").addEventListener("click", function () {
    cancelled = true;
    if (currentXhr) currentXhr.abort();
  });

  window.addEventListener("beforeunload", function (e) {
    if (uploading) {
      e.preventDefault();
      e.returnValue = "";
    }
  });

  /* ---------------- Arrastar e soltar ---------------- */

  var dropOverlay = $("drop-overlay");
  var dragDepth = 0;

  function hasFiles(e) {
    return (
      !!e.dataTransfer &&
      Array.prototype.indexOf.call(e.dataTransfer.types || [], "Files") !== -1
    );
  }

  function droppedFiles(dataTransfer) {
    var files = [];
    var items = dataTransfer.items;

    if (items && items.length) {
      for (var i = 0; i < items.length; i++) {
        var item = items[i];
        if (item.kind !== "file") continue;

        // Pastas arrastadas são ignoradas; só arquivos são enviados.
        var entry = item.webkitGetAsEntry && item.webkitGetAsEntry();
        if (entry && !entry.isFile) continue;

        var file = item.getAsFile();
        if (file) files.push(file);
      }
      return files;
    }

    return Array.prototype.slice.call(dataTransfer.files);
  }

  window.addEventListener("dragenter", function (e) {
    if (!hasFiles(e) || uploading) return;
    e.preventDefault();
    dragDepth++;
    dropOverlay.hidden = false;
  });

  window.addEventListener("dragover", function (e) {
    if (hasFiles(e)) e.preventDefault();
  });

  window.addEventListener("dragleave", function (e) {
    if (!hasFiles(e)) return;
    dragDepth = Math.max(0, dragDepth - 1);
    if (dragDepth === 0) dropOverlay.hidden = true;
  });

  window.addEventListener("drop", function (e) {
    if (!hasFiles(e)) return;

    e.preventDefault();
    dragDepth = 0;
    dropOverlay.hidden = true;

    if (uploading) return;

    var files = droppedFiles(e.dataTransfer);
    if (files.length > 0) startUpload(files);
  });

  /* ---------------- Seleção múltipla / Ações em lote ---------------- */

  var bulkToolbar = $("bulk-toolbar");
  var bulkCount = $("bulk-count");
  var bulkDownloadBtn = $("bulk-download");
  var bulkDeleteBtn = $("bulk-delete");
  var bulkCancelBtn = $("bulk-cancel");

  var selectedItems = new Set();

  function getItemKey(itemEl) {
    var path = itemEl.dataset.path;
    var kind = itemEl.dataset.kind;
    return kind + ":" + path;
  }

  function updateBulkToolbar() {
    var count = selectedItems.size;
    if (count > 0) {
      bulkCount.textContent = count + " " + plural(count, "item selecionado", "itens selecionados");
      bulkToolbar.hidden = false;
    } else {
      bulkToolbar.hidden = true;
    }
  }

  function updateItemSelection(itemEl, selected) {
    var card = itemEl.closest(".file-card");
    if (card) {
      card.classList.toggle("selected", selected);
    }
  }

  function selectItem(itemEl) {
    var key = getItemKey(itemEl);
    if (!selectedItems.has(key)) {
      selectedItems.add(key);
      itemEl.checked = true;
      updateItemSelection(itemEl, true);
    }
  }

  function deselectItem(itemEl) {
    var key = getItemKey(itemEl);
    if (selectedItems.has(key)) {
      selectedItems.delete(key);
      itemEl.checked = false;
      updateItemSelection(itemEl, false);
    }
  }

  function toggleItem(itemEl) {
    if (itemEl.checked) {
      selectItem(itemEl);
    } else {
      deselectItem(itemEl);
    }
    updateBulkToolbar();
  }

  function clearSelection() {
    fileList.querySelectorAll(".select-item").forEach(function (checkbox) {
      if (selectedItems.has(getItemKey(checkbox))) {
        checkbox.checked = false;
        updateItemSelection(checkbox, false);
      }
    });
    selectedItems.clear();
    updateBulkToolbar();
  }

  function getSelectedItems() {
    var items = [];
    selectedItems.forEach(function (key) {
      var parts = key.split(":");
      var kind = parts[0];
      var path = parts.slice(1).join(":");
      items.push({ kind: kind, path: path });
    });
    return items;
  }

  // Delegação de eventos para checkboxes
  fileList.addEventListener("change", function (e) {
    if (e.target.matches(".select-item")) {
      toggleItem(e.target);
    }
  });

  // Seleção via clique no card (Shift+click para seleção em intervalo)
  var lastSelectedCard = null;
  fileList.addEventListener("click", function (e) {
    var card = e.target.closest(".file-card");
    if (!card) return;

    if (longPressedCard === card) {
      e.preventDefault();
      e.stopImmediatePropagation();
      longPressedCard = null;
      clearTimeout(longPressClickTimer);
      return;
    }

    var checkbox = card.querySelector(".select-item");
    if (!checkbox) return;

    if (e.target === checkbox || e.target.closest("button") || e.target.closest("label")) return;

    var clickedLink = e.target.closest("a");
    var mobileSelection = window.matchMedia("(max-width: 768px)").matches && selectedItems.size > 0;
    if (clickedLink && !mobileSelection) return;
    if (clickedLink) e.preventDefault();

    if (e.shiftKey && lastSelectedCard) {
      // Seleção em intervalo
      var cards = Array.prototype.slice.call(fileList.querySelectorAll(".file-card"));
      var start = cards.indexOf(lastSelectedCard);
      var end = cards.indexOf(card);
      if (start > end) { var tmp = start; start = end; end = tmp; }
      for (var i = start; i <= end; i++) {
        var cb = cards[i].querySelector(".select-item");
        if (cb && !cb.checked) selectItem(cb);
      }
      updateBulkToolbar();
    } else {
      // Toggle simples
      checkbox.checked = !checkbox.checked;
      toggleItem(checkbox);
    }
    lastSelectedCard = card;
  });

  fileList.addEventListener("click", async function (e) {
    var button = e.target.closest(".star-button");
    if (!button) return;

    e.preventDefault();
    var starred = button.dataset.starred !== "true";
    button.disabled = true;

    try {
      var response = await fetch("/star", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          kind: button.dataset.kind,
          path: button.dataset.path,
          starred: starred,
        }),
      });

      if (!response.ok) {
        var err = await response.text();
        alert("Erro ao atualizar estrela: " + (err || response.statusText));
        return;
      }

      var result = await response.json();
      button.dataset.starred = String(result.starred);
      button.classList.toggle("starred", result.starred);
      button.setAttribute("aria-pressed", String(result.starred));
      button.title = result.starred ? "Remover estrela" : "Adicionar estrela";
      button.setAttribute(
        "aria-label",
        (result.starred ? "Remover estrela de " : "Adicionar estrela a ") + button.dataset.name
      );
      var label = button.querySelector(".mobile-action-label");
      if (label) {
        label.textContent = result.starred ? "Remover estrela" : "Adicionar estrela";
      }

      if ($("view-input").value === "starred" && !result.starred) {
        refreshList();
      }
    } catch (err) {
      alert("Falha ao atualizar estrela: " + err.message);
    } finally {
      button.disabled = false;
    }
  });

  /* ---------------- Visualizador de fotos ---------------- */

  var photoViewer = $("photo-viewer");
  var photoViewerImage = $("photo-viewer-image");
  var photoViewerName = $("photo-viewer-name");
  var photoViewerMeta = $("photo-viewer-meta");
  var photoViewerStar = $("photo-viewer-star");
  var photoViewerDownload = $("photo-viewer-download");
  var photoViewerDelete = $("photo-viewer-delete");
  var activePhoto = null;

  function syncPhotoViewerStar(starred) {
    photoViewerStar.dataset.starred = String(starred);
    photoViewerStar.classList.toggle("starred", starred);
    photoViewerStar.setAttribute("aria-pressed", String(starred));
    photoViewerStar.setAttribute(
      "aria-label",
      starred ? "Remover estrela" : "Adicionar estrela"
    );
    var label = photoViewerStar.querySelector("span");
    if (label) label.textContent = starred ? "Com estrela" : "Estrela";
    if (activePhoto) activePhoto.dataset.starred = String(starred);
  }

  fileList.addEventListener("click", function (e) {
    var button = e.target.closest(".photo-open");
    if (!button) return;

    if (window.matchMedia("(max-width: 768px)").matches && selectedItems.size > 0) {
      e.preventDefault();
      var checkbox = button.closest(".file-card").querySelector(".select-item");
      checkbox.checked = !checkbox.checked;
      toggleItem(checkbox);
      return;
    }

    activePhoto = button;
    photoViewerImage.src = button.dataset.url;
    photoViewerImage.alt = button.dataset.name;
    photoViewerName.textContent = button.dataset.name;
    photoViewerMeta.textContent = [
      button.dataset.date,
      button.dataset.size,
      button.dataset.location,
    ].filter(Boolean).join(" · ");
    photoViewerDownload.href = button.dataset.url + "?download=1";
    photoViewerDownload.download = button.dataset.name;
    photoViewerStar.dataset.kind = "file";
    photoViewerStar.dataset.path = button.dataset.path;
    syncPhotoViewerStar(button.dataset.starred === "true");
    photoViewer.showModal();
  });

  closeOnBackdrop(photoViewer);
  $("photo-viewer-close").addEventListener("click", function () {
    photoViewer.close();
  });
  photoViewer.addEventListener("close", function () {
    activePhoto = null;
    photoViewerImage.removeAttribute("src");
  });

  photoViewerStar.addEventListener("click", async function () {
    if (!activePhoto) return;

    var starred = photoViewerStar.dataset.starred !== "true";
    photoViewerStar.disabled = true;
    try {
      var response = await fetch("/star", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          kind: "file",
          path: activePhoto.dataset.path,
          starred: starred,
        }),
      });
      if (!response.ok) {
        var error = await response.text();
        alert("Erro ao atualizar estrela: " + (error || response.statusText));
        return;
      }

      var result = await response.json();
      syncPhotoViewerStar(result.starred);
    } catch (error) {
      alert("Falha ao atualizar estrela: " + error.message);
    } finally {
      photoViewerStar.disabled = false;
    }
  });

  photoViewerDelete.addEventListener("click", async function () {
    if (!activePhoto) return;
    var photo = activePhoto;
    if (!(await confirmDeletion(photo.dataset.name, false, false))) return;

    photoViewerDelete.disabled = true;
    try {
      var response = await fetch("/delete?file=" + encodeURIComponent(photo.dataset.path), {
        method: "DELETE",
      });
      if (!response.ok) {
        var error = await response.text();
        alert("Erro ao mover para a lixeira: " + (error || response.statusText));
        return;
      }

      photoViewer.close();
      refreshList();
    } catch (error) {
      alert("Falha ao mover para a lixeira: " + error.message);
    } finally {
      photoViewerDelete.disabled = false;
    }
  });

  function closeMobileMenus() {
    fileList.classList.remove("menu-open");
    document.querySelectorAll(".file-card.menu-open").forEach(function (card) {
      card.classList.remove("menu-open");
      var toggle = card.querySelector(".mobile-menu-toggle");
      if (toggle) toggle.setAttribute("aria-expanded", "false");
    });
  }

  document.body.addEventListener("click", function (e) {
    var toggle = e.target.closest(".mobile-menu-toggle");
    if (toggle) {
      e.preventDefault();
      var card = toggle.closest(".file-card");
      var wasOpen = card.classList.contains("menu-open");
      closeMobileMenus();
      if (!wasOpen) {
        card.classList.add("menu-open");
        fileList.classList.add("menu-open");
        toggle.setAttribute("aria-expanded", "true");
      }
      return;
    }

    if (e.target.closest(".mobile-action-menu")) {
      closeMobileMenus();
      return;
    }
    closeMobileMenus();
  });

  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") {
      closeMobileMenus();
      if (document.querySelector("dialog[open]")) return;
      if (selectedItems.size > 0) {
        e.preventDefault();
        clearSelection();
      }
      return;
    }

    if (e.key === "/" && !$("library-view").hidden &&
        !e.target.closest("input, textarea, select, [contenteditable='true']")) {
      if (document.querySelector("dialog[open]")) return;
      e.preventDefault();
      searchInput.focus();
      return;
    }

    if (!(e.ctrlKey || e.metaKey) || e.key.toLowerCase() !== "a") return;
    if (e.target.closest("input, textarea, select, [contenteditable='true']")) return;
    if (document.querySelector("dialog[open]")) return;

    var checkboxes = fileList.querySelectorAll(".select-item");
    if (checkboxes.length === 0) return;

    e.preventDefault();
    checkboxes.forEach(function (checkbox) {
      selectItem(checkbox);
    });
    updateBulkToolbar();
  });

  fileList.addEventListener("click", async function (e) {
    var button = e.target.closest(".mobile-folder-download");
    if (!button) return;
    e.preventDefault();
    button.disabled = true;
    try {
      var response = await fetch("/bulk-download", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          items: [{ kind: "folder", path: button.dataset.path }],
        }),
      });
      if (!response.ok) {
        var errorText = await response.text();
        alert("Erro ao baixar a pasta: " + (errorText || response.statusText));
        return;
      }
      var blob = await response.blob();
      var url = URL.createObjectURL(blob);
      var link = document.createElement("a");
      link.href = url;
      link.download = (button.dataset.name || "pasta") + ".zip";
      document.body.appendChild(link);
      link.click();
      link.remove();
      URL.revokeObjectURL(url);
    } catch (err) {
      alert("Falha ao baixar a pasta: " + err.message);
    } finally {
      button.disabled = false;
    }
  });

  bulkCancelBtn.addEventListener("click", clearSelection);

  // Download em lote
  bulkDownloadBtn.addEventListener("click", async function () {
    var items = getSelectedItems();
    if (items.length === 0) return;

    if (items.length === 1) {
      // Um único item: usa o link direto
      var item = items[0];
      var url = "/media/" + encodeURIComponent(item.path) + "?download=1";
      window.open(url, "_blank");
      return;
    }

    // Múltiplos itens: solicita ZIP ao servidor
    bulkDownloadBtn.disabled = true;
    bulkDownloadBtn.textContent = " Preparando...";

    try {
      var response = await fetch("/bulk-download", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ items: items }),
      });

      if (response.ok) {
        var blob = await response.blob();
        var url = URL.createObjectURL(blob);
        var a = document.createElement("a");
        a.href = url;
        a.download = "gollery-selecao-" + Date.now() + ".zip";
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        URL.revokeObjectURL(url);
      } else {
        var err = await response.text();
        alert("Erro ao criar ZIP: " + (err || response.statusText));
      }
    } catch (err) {
      alert("Falha de conexão: " + err.message);
    } finally {
      bulkDownloadBtn.disabled = false;
      bulkDownloadBtn.innerHTML = '<i class="icon-download"></i> Baixar';
    }
  });

  // Move os itens selecionados para a lixeira.
  bulkDeleteBtn.addEventListener("click", async function () {
    var items = getSelectedItems();
    if (items.length === 0) return;

    var confirmMsg = items.length === 1
      ? (items[0].kind === "folder" ? "Mover esta pasta e todo o seu conteúdo para a lixeira?" : "Mover este arquivo para a lixeira?")
      : "Mover " + items.length + " itens para a lixeira?";

    if (!confirm(confirmMsg + " Os itens serão excluídos definitivamente após 30 dias.")) return;

    bulkDeleteBtn.disabled = true;
    bulkDeleteBtn.textContent = " Movendo...";

    try {
      var response = await fetch("/bulk-delete", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ items: items }),
      });

      if (response.ok) {
        clearSelection();
        refreshList();
      } else {
        var err = await response.text();
        alert("Erro ao mover para a lixeira: " + (err || response.statusText));
      }
    } catch (err) {
      alert("Falha de conexão: " + err.message);
    } finally {
      bulkDeleteBtn.disabled = false;
      bulkDeleteBtn.innerHTML = '<i class="icon-trash"></i> Lixeira';
    }
  });

  // Limpa seleção quando a lista é atualizada via HTMX
  document.body.addEventListener("htmx:afterSwap", function (e) {
    if (e.detail.target.id === "file-list") {
      clearSelection();
      closeMobileMenus();
    }
  });

  // Limpa seleção ao navegar (mudança de pasta/pesquisa)
  sortForm.addEventListener("htmx:beforeRequest", function () {
    clearSelection();
  });

  /* ---------------- Mover itens (drag & drop) ---------------- */

  var moveDialog = $("move-dialog");
  var dragSourceCards = [];
  var dragOverFolder = null;
  var breadcrumb = document.querySelector(".breadcrumb");
  var longPressTimer = null;
  var longPressThreshold = 500; // ms
  var isLongPress = false;
  var longPressedCard = null;
  var longPressClickTimer = null;

  closeOnBackdrop(moveDialog);

  /* ---------------- Pinça para ampliar/reduzir a grade de fotos ---------------- */

  var photoColumns = 3;
  var pinchStartDistance = 0;
  var pinchStartColumns = 3;

  function touchDistance(touches) {
    var dx = touches[0].clientX - touches[1].clientX;
    var dy = touches[0].clientY - touches[1].clientY;
    return Math.sqrt(dx * dx + dy * dy);
  }

  function setPhotoColumns(columns) {
    photoColumns = Math.max(3, Math.min(6, columns));
    fileList.style.setProperty("--photo-columns", String(photoColumns));
    fileList.setAttribute("aria-label", "Fotos, " + photoColumns + " colunas");
  }

  fileList.addEventListener("touchstart", function (e) {
    if (!fileList.classList.contains("photo-mosaic") || e.touches.length < 2) return;

    pinchStartDistance = touchDistance(e.touches);
    pinchStartColumns = photoColumns;
    if (longPressTimer) {
      clearTimeout(longPressTimer);
      longPressTimer = null;
    }
    touchDragSource = null;
  }, { passive: true });

  fileList.addEventListener("touchmove", function (e) {
    if (!pinchStartDistance || e.touches.length < 2) return;

    e.preventDefault();
    var distanceChange = pinchStartDistance - touchDistance(e.touches);
    var steps = Math.trunc(distanceChange / 45);
    var columns = Math.max(3, Math.min(6, pinchStartColumns + steps));
    if (columns !== photoColumns) setPhotoColumns(columns);
  }, { passive: false });

  function endPhotoPinch(e) {
    if (!pinchStartDistance || (e.type !== "touchcancel" && e.touches.length >= 2)) return;
    if (e.cancelable) e.preventDefault();
    pinchStartDistance = 0;
  }

  fileList.addEventListener("touchend", endPhotoPinch, { passive: false });
  fileList.addEventListener("touchcancel", endPhotoPinch, { passive: false });

  function isInvalidMoveTarget(items, targetPath) {
    return items.some(function (item) {
      return item.kind === "folder" &&
        (item.path === targetPath || targetPath.startsWith(item.path + "/"));
    });
  }

  function confirmMove(itemCount, targetFolderName) {
    return new Promise(function (resolve) {
      var title = itemCount === 1 ? "Mover item?" : "Mover " + itemCount + " itens?";
      var text = "Mover para a pasta “" + targetFolderName + "”?";

      $("move-title").textContent = title;
      $("move-text").textContent = text;

      moveDialog.returnValue = "";
      moveDialog.addEventListener(
        "close",
        function () {
          resolve(moveDialog.returnValue === "confirm");
        },
        { once: true }
      );
      moveDialog.showModal();
    });
  }

  // Drag start - inicia arrasto de itens selecionados ou do item atual
  fileList.addEventListener("dragstart", function (e) {
    var card = e.target.closest(".file-card");
    if (!card) return;

    // Não permite arrastar se está arrastando arquivos do sistema (upload)
    if (uploading) {
      e.preventDefault();
      return;
    }

    var checkbox = card.querySelector(".select-item");
    if (!checkbox) return;

    // Arrastar um item selecionado move a seleção toda; sem seleção, move só este item.
    if (selectedItems.size > 0 && checkbox.checked) {
      dragSourceCards = getSelectedItems();
    } else {
      dragSourceCards = [{ kind: checkbox.dataset.kind, path: checkbox.dataset.path }];
    }

    if (dragSourceCards.length === 0) {
      e.preventDefault();
      return;
    }

    // Define dados do drag
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", JSON.stringify(dragSourceCards));

    // Adiciona classe visual
    card.classList.add("dragging");
    dragSourceCards.forEach(function (item) {
      var selCard = document.querySelector('.select-item[data-kind="' + item.kind + '"][data-path="' + item.path + '"]');
      if (selCard) selCard.closest(".file-card").classList.add("dragging");
    });
  });

  fileList.addEventListener("dragend", function (e) {
    // Remove classes visuais
    document.querySelectorAll(".file-card.dragging").forEach(function (c) {
      c.classList.remove("dragging");
    });
    document.querySelectorAll(".file-card.drag-over").forEach(function (c) {
      c.classList.remove("drag-over");
    });
    breadcrumb.querySelectorAll(".drag-over").forEach(function (c) {
      c.classList.remove("drag-over");
    });
    dragSourceCards = [];
    dragOverFolder = null;
  });

  // Drag over - apenas pastas podem receber drop
  fileList.addEventListener("dragover", function (e) {
    var folderCard = e.target.closest(".folder-card");
    if (!folderCard) return;

    var checkbox = folderCard.querySelector(".select-item");
    if (!checkbox || checkbox.dataset.kind !== "folder") return;

    // Não permite soltar na própria pasta ou em subpastas dos itens arrastados
    var targetPath = checkbox.dataset.path;
    if (isInvalidMoveTarget(dragSourceCards, targetPath)) {
      e.dataTransfer.dropEffect = "none";
      return;
    }

    e.preventDefault();
    e.dataTransfer.dropEffect = "move";

    // Highlight visual
    if (dragOverFolder !== folderCard) {
      document.querySelectorAll(".file-card.drag-over").forEach(function (c) {
        c.classList.remove("drag-over");
      });
      folderCard.classList.add("drag-over");
      dragOverFolder = folderCard;
    }
  });

  fileList.addEventListener("dragleave", function (e) {
    var folderCard = e.target.closest(".folder-card");
    if (folderCard && !folderCard.contains(e.relatedTarget)) {
      folderCard.classList.remove("drag-over");
      if (dragOverFolder === folderCard) dragOverFolder = null;
    }
  });

  // Drop - move itens para a pasta
  fileList.addEventListener("drop", function (e) {
    var folderCard = e.target.closest(".folder-card");
    if (!folderCard) return;

    var checkbox = folderCard.querySelector(".select-item");
    if (!checkbox || checkbox.dataset.kind !== "folder") return;

    e.preventDefault();

    var targetPath = checkbox.dataset.path;
    var targetName = checkbox.dataset.name;
    var itemsToMove = dragSourceCards.slice();

    // Valida se não está tentando mover para si mesmo ou descendentes
    if (isInvalidMoveTarget(itemsToMove, targetPath)) {
      folderCard.classList.remove("drag-over");
      dragOverFolder = null;
      return;
    }

    // Confirma a ação
    confirmMove(itemsToMove.length, targetName).then(function (confirmed) {
      if (!confirmed) {
        folderCard.classList.remove("drag-over");
        dragOverFolder = null;
        return;
      }

      // Executa o move
      moveItems(itemsToMove, targetPath).then(function (success) {
        folderCard.classList.remove("drag-over");
        dragOverFolder = null;
        if (success) {
          clearSelection();
          refreshList();
        }
      });
    });
  });

  breadcrumb.addEventListener("dragover", function (e) {
    var target = e.target.closest("[data-drop-path]");
    if (!target || dragSourceCards.length === 0) return;

    var targetPath = target.dataset.dropPath;
    if (isInvalidMoveTarget(dragSourceCards, targetPath)) {
      e.dataTransfer.dropEffect = "none";
      return;
    }

    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    target.classList.add("drag-over");
  });

  breadcrumb.addEventListener("dragleave", function (e) {
    var target = e.target.closest("[data-drop-path]");
    if (target && !target.contains(e.relatedTarget)) {
      target.classList.remove("drag-over");
    }
  });

  breadcrumb.addEventListener("drop", function (e) {
    var target = e.target.closest("[data-drop-path]");
    if (!target || dragSourceCards.length === 0) return;

    e.preventDefault();
    var itemsToMove = dragSourceCards.slice();
    var targetPath = target.dataset.dropPath;
    var targetName = target.dataset.dropName || target.textContent.trim() || "Início";
    target.classList.remove("drag-over");

    if (isInvalidMoveTarget(itemsToMove, targetPath)) return;

    confirmMove(itemsToMove.length, targetName).then(function (confirmed) {
      if (!confirmed) return;

      moveItems(itemsToMove, targetPath).then(function (success) {
        if (success) {
          clearSelection();
          refreshList();
        }
      });
    });
  });

  async function moveItems(items, targetPath) {
    try {
      var response = await fetch("/bulk-move", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ items: items, targetPath: targetPath }),
      });

      if (response.ok) {
        return true;
      } else {
        var err = await response.text();
        alert("Erro ao mover: " + (err || response.statusText));
        return false;
      }
    } catch (err) {
      alert("Falha de conexão: " + err.message);
      return false;
    }
  }

  /* ---------------- Long press para seleção (mobile) ---------------- */

  fileList.addEventListener("touchstart", function (e) {
    if (e.touches.length > 1) return;
    var card = e.target.closest(".file-card");
    if (!card) return;

    var checkbox = card.querySelector(".select-item");
    if (!checkbox) return;

    // Se já está selecionado, não inicia long press
    if (checkbox.checked) return;

    // O botão da miniatura também permite selecionar por toque prolongado.
    if ((e.target.closest("button") && !e.target.closest(".photo-open")) ||
        e.target.closest("label") || e.target === checkbox) return;

    isLongPress = false;
    longPressTimer = setTimeout(function () {
      isLongPress = true;
      checkbox.checked = true;
      toggleItem(checkbox);
      longPressedCard = card;
      clearTimeout(longPressClickTimer);
      longPressClickTimer = setTimeout(function () {
        longPressedCard = null;
      }, 400);
      // Feedback tátil
      if (navigator.vibrate) navigator.vibrate(50);
    }, longPressThreshold);
  });

  fileList.addEventListener("touchend", function (e) {
    if (longPressTimer) {
      clearTimeout(longPressTimer);
      longPressTimer = null;
    }

    // Se foi long press, impede o click normal
    if (isLongPress) {
      isLongPress = false;
      e.preventDefault();
    }
  });

  fileList.addEventListener("touchmove", function (e) {
    // Cancela long press se moveu o dedo
    if (longPressTimer) {
      clearTimeout(longPressTimer);
      longPressTimer = null;
    }
  });

  /* ---------------- Touch drag para mobile (arrastar para mover) ---------------- */

  var touchDragSource = null;
  var touchDragStartX = 0;
  var touchDragStartY = 0;
  var touchDragThreshold = 10; // pixels

  fileList.addEventListener("touchstart", function (e) {
    if (isLongPress || e.touches.length > 1) return; // Não inicia drag em gesto de pinça

    var card = e.target.closest(".file-card");
    if (!card) return;

    var checkbox = card.querySelector(".select-item");
    if (!checkbox) return;

    // Não inicia drag se tocou em botão ou checkbox.
    if (e.target.closest("button") || e.target.closest("label") || e.target === checkbox) return;

    touchDragSource = {
      card: card,
      checkbox: checkbox,
      startX: e.touches[0].clientX,
      startY: e.touches[0].clientY,
    };
  }, { passive: true });

  fileList.addEventListener("touchmove", function (e) {
    if (!touchDragSource) return;

    var deltaX = e.touches[0].clientX - touchDragSource.startX;
    var deltaY = e.touches[0].clientY - touchDragSource.startY;
    var distance = Math.sqrt(deltaX * deltaX + deltaY * deltaY);

    if (distance > touchDragThreshold) {
      // Inicia drag visual
      touchDragSource.card.classList.add("dragging");
      e.preventDefault(); // Impede scroll
    }
  }, { passive: false });

  fileList.addEventListener("touchend", function (e) {
    if (!touchDragSource) return;

    var card = touchDragSource.card;
    var checkbox = touchDragSource.checkbox;

    card.classList.remove("dragging");

    // Verifica se soltou sobre uma pasta ou breadcrumb
    var touch = e.changedTouches[0];
    var targetEl = document.elementFromPoint(touch.clientX, touch.clientY);
    var folderCard = targetEl && targetEl.closest(".folder-card");
    var crumbTarget = targetEl && targetEl.closest("[data-drop-path]");

    if (folderCard || crumbTarget) {
      var folderCheckbox = folderCard && folderCard.querySelector(".select-item");
      if (crumbTarget || (folderCheckbox && folderCheckbox.dataset.kind === "folder")) {
        var targetPath = crumbTarget ? crumbTarget.dataset.dropPath : folderCheckbox.dataset.path;
        var targetName = crumbTarget
          ? (crumbTarget.dataset.dropName || crumbTarget.textContent.trim() || "Início")
          : folderCheckbox.dataset.name;

        // Determina itens a mover
        var itemsToMove = selectedItems.size > 0 && checkbox.checked
          ? getSelectedItems()
          : [{ kind: checkbox.dataset.kind, path: checkbox.dataset.path }];

        if (!isInvalidMoveTarget(itemsToMove, targetPath)) {
          confirmMove(itemsToMove.length, targetName).then(function (confirmed) {
            if (confirmed) {
              moveItems(itemsToMove, targetPath).then(function (success) {
                if (success) {
                  clearSelection();
                  refreshList();
                }
              });
            }
          });
        }
      }
    }

    touchDragSource = null;
  });

  // Cleanup touch drag no cancelamento
  window.addEventListener("touchcancel", function () {
    if (touchDragSource) {
      touchDragSource.card.classList.remove("dragging");
      touchDragSource = null;
    }
    if (longPressTimer) {
      clearTimeout(longPressTimer);
      longPressTimer = null;
    }
  });
})();
