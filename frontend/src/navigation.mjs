// One workspace menu is shared by every page; page-specific status and footer
// content stays in each page's sidebar.
export const workspaceLinks = Object.freeze([
  { key: "agent", label: "Agent workspace", href: "./", icon: "✦" },
  { key: "graphs", label: "Graph runtime", href: "graphs.html", icon: "◇" },
  { key: "claims", label: "Sample claims", href: "claims.html", icon: "▦" },
  { key: "memory", label: "Learning memory", href: "./#learning", icon: "◎" },
]);

export function currentWorkspaceLink(page, hash = "") {
  return page === "agent" && hash === "#learning" ? "memory" : page;
}

function mountWorkspaceNavigation() {
  const menus = [...document.querySelectorAll("[data-workspace-nav]")];
  for (const menu of menus) {
    const links = workspaceLinks.map((item) => {
      const link = document.createElement("a");
      link.className = "nav-link";
      // A fragment preserves the selected session query on the agent page;
      // links from the other pages still resolve to the agent memory section.
      link.href = item.key === "memory" && menu.dataset.page === "agent" ? "#learning" : item.href;
      link.dataset.workspaceLink = item.key;
      const icon = document.createElement("span");
      icon.setAttribute("aria-hidden", "true");
      icon.textContent = item.icon;
      link.append(icon, document.createTextNode(item.label));
      return link;
    });
    menu.replaceChildren(...links);
  }
  const updateCurrent = () => {
    for (const menu of menus) {
      const current = currentWorkspaceLink(menu.dataset.page, location.hash);
      for (const link of menu.querySelectorAll("[data-workspace-link]")) {
        const selected = link.dataset.workspaceLink === current;
        link.classList.toggle("active", selected);
        if (selected) link.setAttribute("aria-current", current === "memory" ? "location" : "page");
        else link.removeAttribute("aria-current");
      }
    }
  };
  updateCurrent();
  window.addEventListener("hashchange", updateCurrent);
}

if (typeof document !== "undefined") mountWorkspaceNavigation();
