// Keep the hidden customer_id in sync with the visible autocomplete value.
// A blank value deliberately means "unassigned"; a non-empty value that is
// not an exact suggestion is rejected by the server rather than silently
// becoming an orphan.
(function () {
  "use strict";
  var input = document.querySelector("[data-customer-autocomplete]");
  if (!input) return;
  var id = document.getElementById("customer_id");
  var list = document.getElementById(input.getAttribute("list"));
  if (!id || !list) return;

  function sync() {
    var value = input.value.trim().toLowerCase();
    var matches = [];
    if (value) {
      for (var i = 0; i < list.options.length; i++) {
        var option = list.options[i];
        if (option.value.trim().toLowerCase() === value) {
          matches.push(option.getAttribute("data-id") || "");
        }
      }
    }
    // Only an unambiguous suggestion sets the hidden ID. Two customers with
    // the same name leave it blank so the server refuses the ambiguous name
    // instead of silently choosing the first one.
    id.value = matches.length === 1 ? matches[0] : "";
  }

  input.addEventListener("input", sync);
  input.addEventListener("change", sync);
})();
