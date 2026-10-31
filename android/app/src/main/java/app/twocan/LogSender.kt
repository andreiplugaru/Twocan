package app.twocan

import kotlin.coroutines.cancellation.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch

/**
 * Sends the log to the helper and keeps the outcome. It lives as long as
 * [scope] (the app's), not a screen: turning the phone while a log is on its
 * way neither cancels it nor loses its code.
 */
class LogSender(private val scope: CoroutineScope, private val upload: suspend (String) -> String) {
    sealed interface State {
        data object Idle : State
        data object Sending : State
        data class Sent(val code: String) : State
        data class Failed(val message: String) : State
    }

    private val _state = MutableStateFlow<State>(State.Idle)
    val state: StateFlow<State> = _state.asStateFlow()

    /** Sends the log with [note]; does nothing while a log is on its way. */
    fun send(note: String) {
        if (_state.value == State.Sending) return
        _state.value = State.Sending
        scope.launch {
            _state.value = try {
                State.Sent(upload(note))
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                State.Failed(e.userMessage)
            }
        }
    }

    /** Forgets the outcome, once it has been shown. */
    fun reset() {
        if (_state.value != State.Sending) _state.value = State.Idle
    }
}
