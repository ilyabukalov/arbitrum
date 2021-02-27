import { HardhatRuntimeEnvironment } from 'hardhat/types'
import { DeployFunction } from 'hardhat-deploy/types'

const func: DeployFunction = async (hre: HardhatRuntimeEnvironment) => {
  const { deployments, getNamedAccounts } = hre
  const { deploy } = deployments
  const { deployer } = await getNamedAccounts()

  const rollup = await deployments.get('Rollup')
  const challengeFactory = await deployments.get('ChallengeFactory')
  const nodeFactory = await deployments.get('NodeFactory')

  await deploy('RollupCreator', {
    from: deployer,
    args: [],
  })
}

module.exports = func
module.exports.tags = ['RollupCreator']
module.exports.dependencies = ['Rollup', 'ChallengeFactory', 'NodeFactory']