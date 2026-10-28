package app.twocan

import android.content.Context
import androidx.core.content.edit
import app.twocan.go.mobile.Listener
import app.twocan.go.mobile.Mobile
import app.twocan.go.mobile.Node
import java.io.File
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext

/**
 * Owns the Go node. Twocan is online while the app is on screen and for
 * [STOP_DELAY_MS] after it leaves: messages sent to you meanwhile wait on the
 * sender's device until you are both online.
 */
class Twocan(context: Context) {
    sealed interface State {
        data object Stopped : State
        data object Starting : State
        data object Running : State
        data class Failed(val error: String) : State
    }

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val lock = Mutex() // serialises start and stop
    private val prefs = context.getSharedPreferences("settings", Context.MODE_PRIVATE)

    /** Identity, contacts, history and the log live here. */
    val dataDir = File(context.filesDir, "twocan")

    @Volatile private var node: Node? = null
    private var stopJob: Job? = null

    private val _state = MutableStateFlow<State>(State.Stopped)
    val state: StateFlow<State> = _state.asStateFlow()
    private val _contacts = MutableStateFlow<List<Contact>>(emptyList())
    val contacts: StateFlow<List<Contact>> = _contacts.asStateFlow()
    private val _displayName = MutableStateFlow("")
    val displayName: StateFlow<String> = _displayName.asStateFlow()
    private val _reachability = MutableStateFlow("checking…")
    val reachability: StateFlow<String> = _reachability.asStateFlow()
    private val _events = MutableSharedFlow<NodeEvent>(extraBufferCapacity = 256)
    /** Messages and delivery receipts, for the open conversation. */
    val events: SharedFlow<NodeEvent> = _events.asSharedFlow()

    /** Helper addresses set in Settings; "" uses the ones built in. */
    var bootstrap: String
        get() = prefs.getString(KEY_BOOTSTRAP, "") ?: ""
        set(value) = prefs.edit { putString(KEY_BOOTSTRAP, value.trim()) }

    private val listener = object : Listener {
        override fun onEvent(json: String) {
            val ev = Json.event(json) ?: return
            when (ev) {
                is NodeEvent.Reachability -> _reachability.value = ev.text
                NodeEvent.ContactsChanged -> scope.launch { refreshContacts() }
                is NodeEvent.MessageArrived -> {
                    scope.launch { refreshContacts() } // unread counts
                    _events.tryEmit(ev)
                }
                is NodeEvent.Delivered, is NodeEvent.Typing -> _events.tryEmit(ev)
                is NodeEvent.Updated -> {
                    scope.launch { refreshContacts() } // a deletion can change unread counts
                    _events.tryEmit(ev)
                }
            }
        }
    }

    /** The app came on screen: start the node, or keep it running. */
    fun onForeground() {
        stopJob?.cancel()
        stopJob = null
        start()
    }

    /** The app left the screen: stop the node after [STOP_DELAY_MS]. */
    fun onBackground() {
        stopJob?.cancel()
        stopJob = scope.launch {
            delay(STOP_DELAY_MS)
            stop()
        }
    }

    fun start() {
        scope.launch {
            lock.withLock {
                if (node != null) return@withLock
                _state.value = State.Starting
                try {
                    val n = Mobile.start(dataDir.path, bootstrap, "info", listener)
                    node = n
                    _displayName.value = n.displayName()
                    _reachability.value = n.reachability()
                    _state.value = State.Running
                } catch (e: Exception) {
                    _state.value = State.Failed(e.message ?: e.toString())
                    return@withLock
                }
            }
            refreshContacts()
        }
    }

    suspend fun stop() {
        lock.withLock {
            val n = node ?: return
            node = null
            _state.value = State.Stopped
            withContext(Dispatchers.IO) { runCatching { n.stop() } }
        }
    }

    /** Stops and starts the node, e.g. after the helper addresses changed. */
    fun restart() {
        scope.launch {
            stop()
            start()
        }
    }

    /**
     * Runs [block] with the node on an IO thread. Throws if the node is not
     * running; the Go core's errors arrive as exceptions whose message is
     * meant for the user.
     */
    suspend fun <T> call(block: (Node) -> T): T = withContext(Dispatchers.IO) {
        block(node ?: throw IllegalStateException("Twocan is not running"))
    }

    suspend fun refreshContacts() {
        runCatching { call { Json.contacts(it.contacts()) } }.onSuccess { _contacts.value = it }
    }

    suspend fun setDisplayName(name: String) {
        call { it.setDisplayName(name) }
        _displayName.value = call { it.displayName() }
    }

    companion object {
        const val STOP_DELAY_MS = 60_000L
        private const val KEY_BOOTSTRAP = "bootstrap"
    }
}

/** The text of an exception from the Go core, for the user. */
val Throwable.userMessage: String get() = message ?: toString()
