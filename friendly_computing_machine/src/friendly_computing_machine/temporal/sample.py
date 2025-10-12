from datetime import timedelta

from temporalio import activity, workflow

# Import activity implementation safely
# TODO - this is probably not needed
with workflow.unsafe.imports_passed_through():
    from friendly_computing_machine.src.friendly_computing_machine.temporal.ai.activity import generate_gemini_response


@activity.defn
def say_hello(response: str) -> str:
    return f"{response}"


@activity.defn
def build_hello_prompt(name: str) -> str:
    return f"Say hello to {name} in a friendly way."


@workflow.defn
class SayHello:
    @workflow.run
    async def run(self, name: str) -> str:
        prompt = await workflow.execute_activity(
            build_hello_prompt, name, schedule_to_close_timeout=timedelta(seconds=5)
        )
        response = await workflow.execute_activity(
            generate_gemini_response,
            prompt,
            schedule_to_close_timeout=timedelta(seconds=5),
        )
        return await workflow.execute_activity(
            say_hello, response, schedule_to_close_timeout=timedelta(seconds=5)
        )
