package app.twocan.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import app.twocan.Json
import app.twocan.Message
import app.twocan.Twocan
import app.twocan.go.mobile.Mobile
import app.twocan.userMessage
import java.text.DateFormat
import java.util.Date
import kotlinx.coroutines.launch

/** Finds messages in every conversation; tapping one opens it there. */
@Composable
fun SearchScreen(twocan: Twocan, onOpen: (peer: String, message: String) -> Unit, onBack: () -> Unit) {
    var query by rememberSaveable { mutableStateOf("") }
    var results by remember { mutableStateOf<List<Message>?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    val contacts by twocan.contacts.collectAsStateWithLifecycle()
    val scope = rememberCoroutineScope()
    val focus = remember { FocusRequester() }
    LaunchedEffect(Unit) { focus.requestFocus() }

    fun search() {
        if (query.isBlank()) return
        scope.launch {
            runCatching { twocan.call { Json.messages(it.search(query)) } }
                .onSuccess { results = it; error = null }
                .onFailure { error = it.userMessage }
        }
    }

    Scaffold(topBar = { BackBar("Search messages", onBack) }) { padding ->
        Column(Modifier.fillMaxSize().padding(padding)) {
            OutlinedTextField(
                value = query,
                onValueChange = { query = it },
                placeholder = { Text("Words to find") },
                singleLine = true,
                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search),
                keyboardActions = KeyboardActions(onSearch = { search() }),
                modifier = Modifier.fillMaxWidth().padding(16.dp).focusRequester(focus),
            )
            error?.let { Text(it, color = MaterialTheme.colorScheme.error, modifier = Modifier.padding(horizontal = 16.dp)) }
            val found = results
            if (found != null && found.isEmpty()) {
                Text("No messages contain \"$query\".", modifier = Modifier.padding(16.dp))
            }
            LazyColumn(Modifier.fillMaxSize()) {
                items(found.orEmpty(), key = { it.peer + it.id }) { m ->
                    val name = contacts.firstOrNull { it.id == m.peer }?.name ?: "Unknown"
                    ListItem(
                        modifier = Modifier.clickable { onOpen(m.peer, m.id) },
                        headlineContent = {
                            Text(plain(m.text), maxLines = 2, overflow = TextOverflow.Ellipsis)
                        },
                        supportingContent = {
                            val who = if (m.out) "You to $name" else name
                            Text("$who · " + DateFormat.getDateTimeInstance(DateFormat.SHORT, DateFormat.SHORT).format(Date(m.at)))
                        },
                    )
                    HorizontalDivider()
                }
            }
        }
    }
}

/** The text without formatting markers. */
private fun plain(text: String): String =
    runCatching { Json.spans(Mobile.format(text)).joinToString("") { it.text } }.getOrDefault(text)
